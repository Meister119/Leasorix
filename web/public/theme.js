// Applies the stored or preferred colour scheme before the app renders.
(function () {
  var stored = null;
  try { stored = localStorage.getItem("leasorix-theme"); } catch (e) {}
  var dark = stored ? stored === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.classList.toggle("dark", dark);
})();
