<script lang="ts">
  // 版面上的一个**槽位**：清单里声明要填它的插件，各得一个空容器。
  //
  // 这是界面给模块的**框架接口**（与 Kind、皮肤令牌同一类东西）：界面决定「顶栏
  // 右侧有一格」，模块决定那一格里是什么。这里不认识任何插件——加一个、摘一个，
  // 这个文件都不用动。
  //
  // 一个插件坏了（import 失败、mount 抛错）只丢它自己那一格，并在控制台说一声：
  // 界面不能因为一个模块的前端写坏了而整页白掉（与卡片 Broken 那条同一个道理）。
  import { onMount } from "svelte";
  import { pluginManifest, pluginHost } from "./api";

  let { at, lang }: { at: string; lang: string } = $props();
  let box: HTMLDivElement;

  onMount(() => {
    const unmounts: Array<() => void> = [];
    let gone = false;
    void (async () => {
      for (const p of await pluginManifest()) {
        if (gone || !p.slots.includes(at)) continue;
        const el = document.createElement("div");
        el.className = "plugin";
        el.dataset.plugin = p.name;
        box.append(el);
        try {
          const mod = await import(/* @vite-ignore */ `${import.meta.env.BASE_URL}${p.entry}`);
          if (gone) return;
          const off = await mod.mount(el, pluginHost(p.name, lang));
          if (typeof off === "function") unmounts.push(off);
        } catch (err) {
          console.warn(`plugin ${p.name} failed to mount in ${at}:`, err);
          el.remove();
        }
      }
    })();
    return () => {
      gone = true;
      for (const off of unmounts) {
        try {
          off();
        } catch {
          /* 卸载失败不该拦住界面换页 */
        }
      }
    };
  });
</script>

<div class="plugin-slot" data-slot={at} bind:this={box}></div>

<style>
  .plugin-slot {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .plugin-slot:empty {
    display: none;
  }
</style>
