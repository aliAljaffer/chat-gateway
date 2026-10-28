const storedTheme = localStorage.getItem("chat-theme");
if (storedTheme === "light" || storedTheme === "dark") {
  document.documentElement.dataset.theme = storedTheme;
}
