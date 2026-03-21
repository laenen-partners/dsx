// Watch Worker — MutationObserver-based SSE lifecycle manager for data-watch subscriptions.
// Scans the DOM for [data-watch] attributes, diffs changes, and manages a hidden
// Datastar SSE connection element that subscribes to the matching watch topics.
(function () {
  "use strict";

  var WATCH_ATTR = "data-watch";
  var CONTAINER_ID = "__ds-watch";
  var DEBOUNCE_MS = 300;

  var currentWatches = new Set();
  var debounceTimer = null;

  function getStreamURL() {
    var meta = document.querySelector('meta[name="stream-url"]');
    if (!meta || !meta.getAttribute("content")) {
      // Issue #6: warn when meta tag is missing so developers notice quickly.
      console.warn(
        "[watch-worker] <meta name=\"stream-url\"> not found — SSE subscriptions disabled"
      );
      return "";
    }
    return meta.getAttribute("content");
  }

  function collectWatches() {
    var set = new Set();
    document.querySelectorAll("[" + WATCH_ATTR + "]").forEach(function (el) {
      var v = el.getAttribute(WATCH_ATTR);
      if (v) set.add(v);
    });
    return set;
  }

  function setsEqual(a, b) {
    if (a.size !== b.size) return false;
    for (var v of a) {
      if (!b.has(v)) return false;
    }
    return true;
  }

  function reconcile() {
    // Issue #6: wrap in try/catch so errors are visible in console.
    try {
      var next = collectWatches();
      if (setsEqual(currentWatches, next)) return;
      currentWatches = next;

      // Remove existing container.
      var old = document.getElementById(CONTAINER_ID);
      if (old) old.remove();

      if (currentWatches.size === 0) return;

      var streamURL = getStreamURL();
      if (!streamURL) return;

      var watchParam = Array.from(currentWatches).join(",");
      var url = streamURL + "?watch=" + encodeURIComponent(watchParam);

      // Create a hidden div that Datastar picks up via its own MutationObserver.
      // Issue #1: signals are now per-domain on each watched element, not here.
      // Issue #4: data-ignore-morph prevents Datastar's Idiomorph from touching this div.
      var div = document.createElement("div");
      div.id = CONTAINER_ID;
      div.style.display = "none";
      div.setAttribute("data-ignore-morph", "");
      div.setAttribute(
        "data-init",
        "@get('" + url + "', {requestCancellation: 'disabled'})"
      );
      document.body.appendChild(div);
    } catch (err) {
      // Issue #6: surface reconcile errors to the developer console.
      console.error("[watch-worker] reconcile error:", err);
    }
  }

  function scheduleReconcile() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(reconcile, DEBOUNCE_MS);
  }

  function startObserver() {
    reconcile();

    var observer = new MutationObserver(function (mutations) {
      var relevant = false;
      for (var i = 0; i < mutations.length; i++) {
        var m = mutations[i];
        if (m.type === "attributes" && m.attributeName === WATCH_ATTR) {
          relevant = true;
          break;
        }
        if (m.type === "childList") {
          for (var j = 0; j < m.addedNodes.length; j++) {
            var node = m.addedNodes[j];
            if (
              node.nodeType === 1 &&
              (node.hasAttribute(WATCH_ATTR) ||
                node.querySelector("[" + WATCH_ATTR + "]"))
            ) {
              relevant = true;
              break;
            }
          }
          if (!relevant) {
            for (var k = 0; k < m.removedNodes.length; k++) {
              var rnode = m.removedNodes[k];
              if (
                rnode.nodeType === 1 &&
                (rnode.hasAttribute(WATCH_ATTR) ||
                  rnode.querySelector("[" + WATCH_ATTR + "]"))
              ) {
                relevant = true;
                break;
              }
            }
          }
        }
        if (relevant) break;
      }
      if (relevant) scheduleReconcile();
    });

    observer.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: [WATCH_ATTR],
    });
  }

  // Wait for document.body to exist before observing.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", startObserver);
  } else {
    startObserver();
  }
})();
