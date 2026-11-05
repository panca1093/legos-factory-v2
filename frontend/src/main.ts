import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./app/App.vue";
import { router } from "./app/router";
import "./styles/global.css";
import "./styles/lg.css";
import "./styles/shell.css";

createApp(App).use(createPinia()).use(router).mount("#app");
