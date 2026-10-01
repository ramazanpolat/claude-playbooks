(function () {
  "use strict";

  // ---- theme toggle, persisted per-viewer only (localStorage) ----
  var root = document.documentElement;
  var toggle = document.querySelector(".theme-toggle");

  function systemPrefersLight() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches;
  }

  function applyTheme(theme) {
    if (theme === "light" || theme === "dark") {
      root.setAttribute("data-theme", theme);
    } else {
      root.removeAttribute("data-theme");
    }
    if (toggle) {
      var effectiveLight = theme === "light" || (theme !== "dark" && systemPrefersLight());
      toggle.textContent = effectiveLight ? "☾" : "☀";
      toggle.setAttribute("aria-label", effectiveLight ? "Switch to dark theme" : "Switch to light theme");
    }
  }

  var stored = null;
  try {
    stored = window.localStorage.getItem("cpb-theme");
  } catch (e) {
    /* private window / blocked storage: fall back to system default */
  }
  applyTheme(stored);

  if (toggle) {
    toggle.addEventListener("click", function () {
      var current = root.getAttribute("data-theme");
      var effectiveLight = current === "light" || (!current && systemPrefersLight());
      var next = effectiveLight ? "dark" : "light";
      applyTheme(next);
      try {
        window.localStorage.setItem("cpb-theme", next);
      } catch (e) {
        /* ignore: per-viewer convenience only */
      }
    });
  }

  // ---- copy buttons on snippet blocks ----
  document.querySelectorAll(".copy-btn").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var targetId = btn.getAttribute("data-copy-target");
      var block = targetId ? document.getElementById(targetId) : btn.closest(".term").querySelector("pre");
      if (!block) return;
      var text = block.innerText;
      var settled = false;
      var done = function () {
        if (settled) return; // the clipboard promise can still land after the fallback already ran
        settled = true;
        var original = btn.textContent;
        btn.textContent = "copied";
        btn.classList.add("copied");
        setTimeout(function () {
          btn.textContent = original;
          btn.classList.remove("copied");
        }, 1400);
      };
      var legacyCopy = function () {
        var ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand("copy"); } catch (e) { /* no-op */ }
        document.body.removeChild(ta);
        done();
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        // A blocked or unanswerable permission prompt can leave this promise
        // pending forever instead of rejecting, so race it against a short
        // timeout and fall back to execCommand rather than leave the button
        // showing "copy" with no feedback.
        navigator.clipboard.writeText(text).then(done, legacyCopy);
        setTimeout(function () {
          if (!settled) legacyCopy();
        }, 800);
      } else {
        legacyCopy();
      }
    });
  });

  // ---- smooth-scroll in-page jumps only, never wheel/trackpad input ----
  var root2 = document.documentElement;
  document.querySelectorAll('a[href^="#"]').forEach(function (a) {
    a.addEventListener("click", function () {
      root2.classList.add("jump-scroll");
      setTimeout(function () { root2.classList.remove("jump-scroll"); }, 700);
    });
  });

  // ---- nav scroll-spy ----
  var navLinks = Array.prototype.slice.call(document.querySelectorAll("nav.site-nav a"));
  var sections = navLinks
    .map(function (a) {
      var id = a.getAttribute("href").replace("#", "");
      return document.getElementById(id);
    })
    .filter(Boolean);

  if (sections.length && "IntersectionObserver" in window) {
    var observer = new IntersectionObserver(
      function (entries) {
        entries.forEach(function (entry) {
          var link = navLinks.find(function (a) {
            return a.getAttribute("href") === "#" + entry.target.id;
          });
          if (!link) return;
          if (entry.isIntersecting) {
            navLinks.forEach(function (a) { a.classList.remove("active"); });
            link.classList.add("active");
          }
        });
      },
      { rootMargin: "-45% 0px -50% 0px", threshold: 0 }
    );
    sections.forEach(function (s) { observer.observe(s); });
  }
})();
