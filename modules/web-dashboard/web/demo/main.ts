import { mount } from "svelte";
import DemoShell from "./DemoShell.svelte";
// **产品那一份 CSS，一行没改。** 卡片那一族样式（.card / .row / .pill …）住在
// 这里，而它们是全局的——真组件在别处渲染时也得带着它们。想要一份「只装卡片那半」
// 的裁剪版是多余的：用不到的规则（.shell / .content / .secbar）在这个页面上要么
// 正好复用，要么因为类名根本没出现而完全无副作用。
import "../src/app.css";
import "./demo.css";

mount(DemoShell, { target: document.getElementById("app")! });
