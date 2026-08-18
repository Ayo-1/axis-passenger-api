// frontend-example.js
// Drop this in your frontend (vanilla JS, Vue, React — same concept)

const sessionId = crypto.randomUUID(); // unique per browser tab

const url = `https://api.yourdomain.com/driver/assign?session_id=${sessionId}`;
const es = new EventSource(url);

es.onmessage = (event) => {
  const data = JSON.parse(event.data);

  if (data.event === "assigned") {
    console.log("Your driver:", data.driver);
    // Show driver info in UI
    // e.g. document.getElementById("driver-name").textContent = data.driver.name;
  }

  if (data.event === "heartbeat") {
    // Connection still alive — no action needed
  }
};

es.onerror = () => {
  console.warn("Connection lost — driver will be released automatically on server");
  es.close();
};

// When the user navigates away, the browser closes the EventSource connection
// which triggers the disconnect on the Go server → driver is released automatically.

// If you want to manually release (e.g. user clicks "Done"):
// es.close();
