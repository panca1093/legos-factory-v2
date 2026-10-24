import { ref, watch } from "vue";
const theme = ref<"light" | "dark">("light");
let initialized = false;
export function useTheme() {
  if (!initialized) {
    try {
      theme.value =
        localStorage.getItem("legos-factory-theme") === "dark"
          ? "dark"
          : "light";
    } catch {
      theme.value = "light";
    }
    initialized = true;
  }
  const apply = () => {
    document.documentElement.dataset.theme = theme.value;
    try {
      localStorage.setItem("legos-factory-theme", theme.value);
    } catch {
      /* Browser storage can be disabled. */
    }
  };
  apply();
  watch(theme, apply);
  return {
    theme,
    toggleTheme: () => {
      theme.value = theme.value === "light" ? "dark" : "light";
    },
  };
}
