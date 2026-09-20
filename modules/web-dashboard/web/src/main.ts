import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

// Svelte 5 的 mount（不是 new App）：组件实例与「挂载」是两件事，后者带目标
// 与 props。写错的表现是页面空白、控制台一句 deprecation，很容易被忽略。
mount(App, { target: document.getElementById("app")! });
