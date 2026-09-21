// Filters the loaded index rows as you type.
//
// A site's index is paginated by the module, so "find my series" would
// otherwise mean paging through dozens of screens. This narrows whatever has
// been loaded; "Load more" brings in further pages, and the filter keeps
// applying to all of them.
(function () {
	function apply(input) {
		var q = input.value.trim().toLowerCase();
		var rows = document.querySelectorAll("#site-rows li.site-row");
		var shown = 0;
		rows.forEach(function (row) {
			var title = (row.dataset.title || "").toLowerCase();
			var hit = q === "" || title.indexOf(q) !== -1;
			row.hidden = !hit;
			if (hit) shown++;
		});
		var note = document.getElementById("filter-note");
		if (note) {
			note.textContent = q === ""
				? ""
				: shown + " of " + rows.length + " loaded titles match";
		}
	}

	document.addEventListener("input", function (e) {
		if (e.target && e.target.id === "site-filter") apply(e.target);
	});

	// Rows arrive after the page does, and again on every "Load more".
	document.body.addEventListener("htmx:afterSwap", function () {
		var input = document.getElementById("site-filter");
		if (input && input.value) apply(input);
	});
})();
