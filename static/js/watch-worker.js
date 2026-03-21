// Watch Worker — MutationObserver-based SSE lifecycle manager for data-watch subscriptions.
// Scans the DOM for [data-watch] attributes, diffs changes, and manages a hidden
// Datastar SSE connection element that subscribes to the matching watch topics.
(function () {
  "use strict";

  const WATCH_ATTR = "data-watch";
  const CONTAINER_ID = "__ds-watch";
  const DEBOUNCE_MS = 300;

  let currentWatches = new Set();
  let debounceTimer = null;

  function getStreamURL() {
    const meta = document.querySelector('meta[name="stream-url"]');
    return meta ? meta.getAttribute("content") : "";
  }

  function collectWatches() {
    const set = new Set();
    document.querySelectorAll("[" + WATCH_ATTR + "]").forEach(function (el) {
      const v = el.getAttribute(WATCH_ATTR);
      if (v) set.add(v);
    });
    return set;
  }

  function setsEqual(a, b) {
    if (a.size !== b.size) return false;
    for (const v of a) {
      if (!b.has(v)) return false;
    }
    return true;
  }

  function reconcile() {
    const next = collectWatches();
    if (setsEqual(currentWatches, next)) return;
    currentWatches = next;

    // Remove existing container.
    const old = document.getElementById(CONTAINER_ID);
    if (old) old.remove();

    if (currentWatches.size === 0) return;

    const streamURL = getStreamURL();
    if (!streamURL) return;

    const watchParam = Array.from(currentWatches).join(",");
    const url = streamURL + "?watch=" + encodeURIComponent(watchParam);

    // Create a hidden div that Datastar picks up via its own MutationObserver.
    const div = document.createElement("div");
    div.id = CONTAINER_ID;
    div.style.display = "none";
    div.setAttribute(
      "data-signals",
      JSON.stringify({ _dsEvent: { domain: "", id: "", action: "", ts: 0 } })
    );
    div.setAttribute(
      "data-init",
      "@get('" + url + "', {requestCancellation: 'disabled'})"
    );
    document.body.appendChild(div);
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
