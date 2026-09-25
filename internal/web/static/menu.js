// Closes an open ⋯ menu on a click outside it or on Escape, as menus do.
// They are <details> elements, which on their own close only from their
// own summary.
(() => {
	const close = (except) => {
		for (const d of document.querySelectorAll("details.menu[open], details.row-menu[open]")) {
			if (!d.contains(except)) d.open = false;
		}
	};
	document.addEventListener("click", (e) => close(e.target));
	document.addEventListener("keydown", (e) => { if (e.key === "Escape") close(null); });
})();
