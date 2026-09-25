// Closes the page's event stream when the page is left. Chrome keeps left
// pages in its back/forward cache with their stream still open, and after a
// few pages its six connections to atsume are all taken and the next page
// never loads. A page restored from that cache reloads, since it missed the
// updates sent while it was away.
(() => {
	let source;
	htmx.createEventSource = (url) => (source = new EventSource(url, { withCredentials: true }));
	addEventListener("pagehide", () => source?.close());
	addEventListener("pageshow", (e) => { if (e.persisted) location.reload(); });
})();
