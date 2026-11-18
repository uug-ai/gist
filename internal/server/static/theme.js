// Sets the theme before the first paint, as draw.uug.ai does: the saved
// choice, or the system preference. Loaded without defer from <head>.
(function () {
  var theme = null;
  try { theme = localStorage.getItem("gist.theme"); } catch (e) { /* storage unavailable */ }
  if (theme !== "dark" && theme !== "light") {
    theme = window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  document.documentElement.setAttribute("data-theme", theme);
})();
