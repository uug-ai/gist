// Bug reports with inspect (https://inspect.uug.ai), on staging only. Reports
// become issues in uug-ai/gist, which is public. Production (gist.uug.ai)
// never loads the script; the same image runs in both, so the gate is here.
(function () {
  "use strict";
  var host = location.hostname;
  if (host !== "uug.lol" && !/\.uug\.lol$/.test(host)) return;
  if (window.gistInspectLoaded) return;
  window.gistInspectLoaded = true;

  var script = document.createElement("script");
  script.src = "https://inspect.uug.ai/static/lib/inspect.js";
  script.async = true;
  script.onload = function () {
    if (!window.UugInspect) return;
    window.UugInspect.createInspect({
      endpoint: "https://inspect.uug.ai",
      repo: "uug-ai/gist",
      getContext: function () { return { app: "gist" }; }
    });
  };
  document.head.appendChild(script);
})();
