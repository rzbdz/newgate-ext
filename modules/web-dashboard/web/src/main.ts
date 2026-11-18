import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

// Svelte 5's mount (not new App): a component instance and "mounting" are two things; the
// latter carries a target and props. A mistake here shows up as a blank page and one
// deprecation line in the console, very easy to overlook.
mount(App, { target: document.getElementById("app")! });
