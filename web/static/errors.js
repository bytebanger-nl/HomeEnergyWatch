// Sends script errors and unhandled promise rejections to the gateway, which
// writes them to its log (hwlocal.log as a service). A classic script loaded
// before app.js, so it also catches errors while the modules load.
(() => {
  const MAX = 10; // per page load
  const seen = new Set();

  function send(report) {
    const key = `${report.kind}|${report.message}|${report.source}|${report.line}`;
    if (seen.has(key) || seen.size >= MAX) return;
    seen.add(key);
    report.page = location.pathname + location.hash;
    try {
      fetch("/api/client-errors", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(report),
        keepalive: true,
      }).catch(() => {});
    } catch {
      // reporting must never cause an error of its own
    }
  }

  const text = (v) => {
    if (v instanceof Error) return `${v.name}: ${v.message}`;
    try {
      return typeof v === "string" ? v : JSON.stringify(v) ?? String(v);
    } catch {
      return String(v);
    }
  };

  window.addEventListener("error", (e) => {
    // an image or other resource that failed to load, not a script error
    if (!(e instanceof ErrorEvent)) return;
    // "Script error." is all a browser tells about another origin's script
    // (an extension, for one): nothing to go on
    if (!e.filename && e.message === "Script error.") return;
    send({
      kind: "error",
      message: e.error ? text(e.error) : String(e.message || ""),
      source: e.filename || "",
      line: e.lineno || 0,
      column: e.colno || 0,
      stack: (e.error && e.error.stack) || "",
    });
  });

  window.addEventListener("unhandledrejection", (e) => {
    const r = e.reason;
    send({
      kind: "rejection",
      message: text(r),
      source: "",
      line: 0,
      column: 0,
      stack: (r && r.stack) || "",
    });
  });
})();
