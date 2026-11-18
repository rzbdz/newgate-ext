import { mount } from "svelte";
import DemoShell from "./DemoShell.svelte";
// **The product's CSS, not one line changed.** The card family of styles (.card / .row /
// .pill …) lives here, and they are global — the real components need them along when
// they render elsewhere. A trimmed "card half only" copy would be pointless: the rules
// that go unused (.shell / .content / .secbar) are either reused as is on this page or
// have no side effect at all because those class names never appear.
import "../src/app.css";
import "./demo.css";

mount(DemoShell, { target: document.getElementById("app")! });
