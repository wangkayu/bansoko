// Anti-flash theming: before the first paint, add the class to <html> based on
//   localStorage/system preference, matching App.tsx's ThemeProvider (storage key "theme").
//   A standalone file rather than an inline script: the page CSP is script-src 'self' and no
//   inline script is allowed.
(function () {
  try {
    var t = localStorage.getItem("theme") || "light";
    var dark = t === "dark" || (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.add(dark ? "dark" : "light");
    document.documentElement.style.colorScheme = dark ? "dark" : "light";
  } catch (e) {}
})();
