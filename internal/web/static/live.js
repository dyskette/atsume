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

	// A live update replaces the status line, bar and all, and a new bar
	// would start its sweep from the left each time. Timing every sweep from
	// the page's own clock puts each new bar where the last one was.
	document.addEventListener("animationstart", (e) => {
		if (e.animationName !== "sweep") return;
		for (const a of e.target.getAnimations()) {
			if (a.animationName === "sweep") a.startTime = 0;
		}
	});
})();
