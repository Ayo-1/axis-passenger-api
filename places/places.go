package places

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// Config controls the proxy.
type Config struct {
	// APIKey: Google Cloud key with Places API (New) and Geocoding API enabled.
	APIKey string
	// Countries: ISO-3166 alpha-2 codes results are restricted to. Default gh, ng, ci.
	Countries []string
	// LanguageCode for labels. Default "en".
	LanguageCode string
	// RatePerMinute per client IP across the three endpoints. Default 20.
	RatePerMinute int
	// CacheTTL for cached Google results. Default 24h.
	CacheTTL time.Duration
	// KeyPrefix for Redis keys. Default "places:".
	KeyPrefix string
	// HTTPClient for upstream calls. Default 5s timeout.
	HTTPClient *http.Client
}

func (c *Config) defaults() {
	if len(c.Countries) == 0 {
		c.Countries = []string{"gh", "ng", "ci"}
	}
	if c.LanguageCode == "" {
		c.LanguageCode = "en"
	}
	if c.RatePerMinute <= 0 {
		c.RatePerMinute = 20
	}
	if c.CacheTTL <= 0 {
		c.CacheTTL = 24 * time.Hour
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "places:"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
}

// Place is the shape returned to the frontend.
type Place struct {
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

// Prediction is one autocomplete row. Coordinates are resolved via /details.
type Prediction struct {
	PlaceID       string `json:"placeId"`
	Label         string `json:"label"`
	MainText      string `json:"mainText"`
	SecondaryText string `json:"secondaryText"`
}

// Service holds shared state.
type Service struct {
	cfg Config
	rdb *redis.Client
}

// New builds a Service. rdb may be nil; caching and limiting are then skipped.
func New(rdb *redis.Client, cfg Config) (*Service, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("places: APIKey is required")
	}
	cfg.defaults()
	return &Service{cfg: cfg, rdb: rdb}, nil
}

// Register mounts the handlers under group at /places/*.
func Register(group *gin.RouterGroup, rdb *redis.Client, cfg Config) (*Service, error) {
	s, err := New(rdb, cfg)
	if err != nil {
		return nil, err
	}
	g := group.Group("/places")
	g.Use(s.limit())
	g.GET("/autocomplete", s.Autocomplete)
	g.GET("/details", s.Details)
	g.GET("/reverse", s.Reverse)
	return s, nil
}

// ---- rate limit (Redis fixed window per IP) ---------------------------------

func (s *Service) limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.rdb == nil {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		key := s.cfg.KeyPrefix + "rl:" + c.ClientIP() + ":" + strconv.FormatInt(time.Now().Unix()/60, 10)
		pipe := s.rdb.TxPipeline()
		incr := pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, 90*time.Second)
		if _, err := pipe.Exec(ctx); err != nil {
			// Redis down: fail open, the group-level limiter still applies.
			c.Next()
			return
		}
		if incr.Val() > int64(s.cfg.RatePerMinute) {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
			return
		}
		c.Header("Cache-Control", "private, max-age=3600")
		c.Next()
	}
}

// ---- cache helpers ----------------------------------------------------------

func (s *Service) cacheGet(ctx context.Context, key string, out any) bool {
	if s.rdb == nil {
		return false
	}
	raw, err := s.rdb.Get(ctx, s.cfg.KeyPrefix+key).Bytes()
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

func (s *Service) cacheSet(ctx context.Context, key string, v any) {
	if s.rdb == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := s.rdb.Set(ctx, s.cfg.KeyPrefix+key, raw, s.cfg.CacheTTL).Err(); err != nil {
		slog.Warn("places: cache set", "error", err)
	}
}

// ---- handlers ---------------------------------------------------------------

var (
	sessionRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	placeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{10,300}$`)
)

// Autocomplete handles GET /places/autocomplete.
func (s *Service) Autocomplete(c *gin.Context) {
	ctx := c.Request.Context()
	q := strings.TrimSpace(c.Query("q"))
	session := c.Query("session")
	if len([]rune(q)) < 3 || len(q) > 120 {
		c.JSON(http.StatusOK, gin.H{"predictions": []Prediction{}})
		return
	}
	if !sessionRe.MatchString(session) {
		session = ""
	}

	key := "ac:" + strings.ToLower(q)
	var preds []Prediction
	if s.cacheGet(ctx, key, &preds) {
		c.JSON(http.StatusOK, gin.H{"predictions": preds, "cached": true})
		return
	}

	body := gin.H{
		"input":                   q,
		"languageCode":            s.cfg.LanguageCode,
		"includedRegionCodes":     s.cfg.Countries,
		"includeQueryPredictions": false,
	}
	if session != "" {
		body["sessionToken"] = session
	}
	var out struct {
		Suggestions []struct {
			PlacePrediction struct {
				PlaceID string `json:"placeId"`
				Text    struct {
					Text string `json:"text"`
				} `json:"text"`
				StructuredFormat struct {
					MainText struct {
						Text string `json:"text"`
					} `json:"mainText"`
					SecondaryText struct {
						Text string `json:"text"`
					} `json:"secondaryText"`
				} `json:"structuredFormat"`
			} `json:"placePrediction"`
		} `json:"suggestions"`
	}
	err := s.google(ctx, http.MethodPost,
		"https://places.googleapis.com/v1/places:autocomplete", body,
		"suggestions.placePrediction.placeId,suggestions.placePrediction.text,suggestions.placePrediction.structuredFormat",
		&out)
	if err != nil {
		slog.Error("places: autocomplete upstream", "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "place search unavailable"})
		return
	}
	preds = make([]Prediction, 0, len(out.Suggestions))
	for _, sg := range out.Suggestions {
		p := sg.PlacePrediction
		if p.PlaceID == "" {
			continue
		}
		preds = append(preds, Prediction{
			PlaceID:       p.PlaceID,
			Label:         p.Text.Text,
			MainText:      p.StructuredFormat.MainText.Text,
			SecondaryText: p.StructuredFormat.SecondaryText.Text,
		})
	}
	s.cacheSet(ctx, key, preds)
	c.JSON(http.StatusOK, gin.H{"predictions": preds})
}

// Details handles GET /places/details.
func (s *Service) Details(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Query("placeId")
	session := c.Query("session")
	if !placeIDRe.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid placeId"})
		return
	}
	key := "pd:" + id
	var cached Place
	if s.cacheGet(ctx, key, &cached) {
		c.JSON(http.StatusOK, cached)
		return
	}
	u := "https://places.googleapis.com/v1/places/" + url.PathEscape(id) +
		"?languageCode=" + url.QueryEscape(s.cfg.LanguageCode)
	if sessionRe.MatchString(session) {
		u += "&sessionToken=" + url.QueryEscape(session)
	}
	var out struct {
		FormattedAddress string `json:"formattedAddress"`
		DisplayName      struct {
			Text string `json:"text"`
		} `json:"displayName"`
		Location struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
	}
	// Basic-data fields only: free inside an autocomplete session.
	if err := s.google(ctx, http.MethodGet, u, nil, "formattedAddress,displayName,location", &out); err != nil {
		slog.Error("places: details upstream", "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "place lookup unavailable"})
		return
	}
	label := out.FormattedAddress
	if out.DisplayName.Text != "" && !strings.HasPrefix(label, out.DisplayName.Text) {
		label = out.DisplayName.Text + ", " + label
	}
	p := Place{Label: label, Lat: out.Location.Latitude, Lng: out.Location.Longitude}
	s.cacheSet(ctx, key, p)
	c.JSON(http.StatusOK, p)
}

// Reverse handles GET /places/reverse.
func (s *Service) Reverse(c *gin.Context) {
	ctx := c.Request.Context()
	lat, err1 := strconv.ParseFloat(c.Query("lat"), 64)
	lng, err2 := strconv.ParseFloat(c.Query("lng"), 64)
	if err1 != nil || err2 != nil || math.Abs(lat) > 90 || math.Abs(lng) > 180 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid coordinates"})
		return
	}
	// ~11 m grid: nearby pin drops share one cache entry.
	key := fmt.Sprintf("rv:%.4f,%.4f", math.Round(lat*1e4)/1e4, math.Round(lng*1e4)/1e4)
	fallback := Place{Label: fmt.Sprintf("%.5f, %.5f", lat, lng), Lat: lat, Lng: lng}
	var cached Place
	if s.cacheGet(ctx, key, &cached) {
		c.JSON(http.StatusOK, cached)
		return
	}
	u := "https://maps.googleapis.com/maps/api/geocode/json?latlng=" +
		url.QueryEscape(fmt.Sprintf("%.6f,%.6f", lat, lng)) +
		"&language=" + url.QueryEscape(s.cfg.LanguageCode) +
		"&result_type=street_address|premise|route|neighborhood|sublocality|locality" +
		"&key=" + url.QueryEscape(s.cfg.APIKey)
	var out struct {
		Status  string `json:"status"`
		Results []struct {
			FormattedAddress string `json:"formatted_address"`
		} `json:"results"`
	}
	if err := s.google(ctx, http.MethodGet, u, nil, "", &out); err != nil {
		slog.Error("places: reverse upstream", "error", err)
		c.JSON(http.StatusOK, fallback)
		return
	}
	if out.Status != "OK" || len(out.Results) == 0 {
		s.cacheSet(ctx, key, fallback)
		c.JSON(http.StatusOK, fallback)
		return
	}
	p := Place{Label: out.Results[0].FormattedAddress, Lat: lat, Lng: lng}
	s.cacheSet(ctx, key, p)
	c.JSON(http.StatusOK, p)
}

// google performs an upstream call. fieldMask is used for Places (New) only.
func (s *Service) google(ctx context.Context, method, u string, body any, fieldMask string, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if fieldMask != "" {
		req.Header.Set("X-Goog-Api-Key", s.cfg.APIKey)
		req.Header.Set("X-Goog-FieldMask", fieldMask)
	}
	res, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("google %s -> %d: %s", u, res.StatusCode, msg)
	}
	return json.Unmarshal(raw, out)
}
