// gist.uug.ai — progressive enhancement. Every page works without this.
(function () {
  "use strict";

  var STORE_KEY = "gist.uug.ai/yours";

  function readYours() {
    try {
      var list = JSON.parse(localStorage.getItem(STORE_KEY) || "[]");
      return Array.isArray(list) ? list : [];
    } catch (e) {
      return [];
    }
  }

  function writeYours(list) {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify(list.slice(0, 50)));
    } catch (e) { /* storage unavailable */ }
  }

  function forget(id) {
    writeYours(readYours().filter(function (g) { return g.id !== id; }));
  }

  // Copy buttons -----------------------------------------------------------
  function flash(button) {
    var label = button.textContent;
    button.textContent = button.getAttribute("data-copied") || "Copied";
    button.classList.add("is-done");
    setTimeout(function () {
      button.textContent = label;
      button.classList.remove("is-done");
    }, 1600);
  }

  function copy(text, button) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () { flash(button); });
      return;
    }
    var area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    try { document.execCommand("copy"); flash(button); } catch (e) { /* ignore */ }
    document.body.removeChild(area);
  }

  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-copy-text], [data-copy-raw]");
    if (!button) return;
    if (button.hasAttribute("data-copy-text")) {
      copy(button.getAttribute("data-copy-text"), button);
      return;
    }
    fetch(button.getAttribute("data-copy-raw"), { credentials: "same-origin" })
      .then(function (r) { return r.text(); })
      .then(function (text) { copy(text, button); });
  });

  // Confirm destructive forms ----------------------------------------------
  document.addEventListener("submit", function (event) {
    var form = event.target;
    var question = form.getAttribute("data-confirm");
    if (question && !window.confirm(question)) {
      event.preventDefault();
      return;
    }
    var id = form.getAttribute("data-forget");
    if (id) forget(id);
  });

  // Highlight the line in the URL fragment (#file-x-L12) -------------------
  function markTarget() {
    document.querySelectorAll(".line.is-target").forEach(function (el) { el.classList.remove("is-target"); });
    var match = /^#(.+-L)(\d+)$/.exec(location.hash);
    if (!match) return;
    var number = document.getElementById(match[1] + match[2]);
    if (!number) return;
    var table = number.closest(".lntable");
    var lines = table ? table.querySelectorAll(".line") : [];
    var line = lines[parseInt(match[2], 10) - 1];
    if (line) line.classList.add("is-target");
  }
  window.addEventListener("hashchange", markTarget);

  // Gist page: remember gists this browser owns ----------------------------
  function rememberOwned() {
    var head = document.querySelector("[data-gist-id][data-owner]");
    if (!head) return;
    var id = head.getAttribute("data-gist-id");
    var list = readYours().filter(function (g) { return g.id !== id; });
    list.unshift({ id: id, title: head.getAttribute("data-gist-title") || id, at: new Date().toISOString() });
    writeYours(list);
  }

  // New gist page: list them -------------------------------------------------
  function showYours() {
    var deleted = new URLSearchParams(location.search).get("deleted");
    if (deleted) {
      forget(deleted);
      history.replaceState(null, "", "/");
    }
    var list = readYours();
    var link = document.querySelector("[data-yours-link]");
    if (link && list.length) link.hidden = false;
    var section = document.querySelector("[data-yours]");
    var ul = document.querySelector("[data-yours-list]");
    if (!section || !ul || !list.length) return;
    list.forEach(function (g) {
      var li = document.createElement("li");
      var a = document.createElement("a");
      a.href = "/" + g.id;
      a.textContent = g.title;
      var time = document.createElement("time");
      time.dateTime = g.at;
      time.textContent = new Date(g.at).toLocaleDateString();
      li.appendChild(a);
      li.appendChild(time);
      ul.appendChild(li);
    });
    section.hidden = false;
  }

  // Editor: add/remove files, tab indentation, language hint ----------------
  var LANGS = {
    js: "JavaScript", mjs: "JavaScript", cjs: "JavaScript", ts: "TypeScript", tsx: "TSX", jsx: "JSX",
    go: "Go", py: "Python", rb: "Ruby", rs: "Rust", java: "Java", kt: "Kotlin", swift: "Swift",
    c: "C", h: "C", cpp: "C++", cc: "C++", hpp: "C++", cs: "C#", php: "PHP", sh: "Shell", bash: "Bash",
    zsh: "Shell", ps1: "PowerShell", sql: "SQL", html: "HTML", css: "CSS", scss: "SCSS", json: "JSON",
    yaml: "YAML", yml: "YAML", toml: "TOML", xml: "XML", md: "Markdown", markdown: "Markdown",
    dockerfile: "Dockerfile", tf: "Terraform", lua: "Lua", dart: "Dart", ex: "Elixir", exs: "Elixir",
    hs: "Haskell", scala: "Scala", r: "R", txt: "Text", log: "Text", diff: "Diff", patch: "Diff",
    ini: "INI", proto: "Protocol Buffer", graphql: "GraphQL", vue: "Vue", svelte: "Svelte"
  };

  function langHint(input) {
    var hint = input.closest("[data-file]").querySelector("[data-lang-hint]");
    var name = input.value.trim().toLowerCase();
    var ext = name.indexOf(".") >= 0 ? name.split(".").pop() : name;
    hint.textContent = LANGS[ext] || "";
  }

  function setupEditor(form) {
    var files = form.querySelector("[data-files]");
    var template = document.querySelector("[data-file-template]");
    var max = parseInt(form.getAttribute("data-max-files"), 10) || 20;
    var addButton = form.querySelector("[data-add-file]");

    function refresh() {
      var count = files.querySelectorAll("[data-file]").length;
      if (count <= 1) files.setAttribute("data-single", "");
      else files.removeAttribute("data-single");
      addButton.disabled = count >= max;
    }

    addButton.addEventListener("click", function () {
      files.appendChild(template.content.cloneNode(true));
      var added = files.lastElementChild;
      added.querySelector(".filename").focus();
      refresh();
    });

    files.addEventListener("click", function (event) {
      var remove = event.target.closest("[data-remove-file]");
      if (!remove) return;
      remove.closest("[data-file]").remove();
      refresh();
    });

    files.addEventListener("input", function (event) {
      if (event.target.matches(".filename")) langHint(event.target);
    });
    files.querySelectorAll(".filename").forEach(langHint);

    // Tab inserts a tab (Shift+Tab outdents); Esc releases focus trapping.
    var escaped = false;
    files.addEventListener("keydown", function (event) {
      var area = event.target;
      if (!area.matches(".code-input")) return;
      if (event.key === "Escape") { escaped = true; return; }
      if (event.key !== "Tab" || escaped || event.ctrlKey || event.metaKey || event.altKey) {
        if (event.key !== "Tab") escaped = false;
        return;
      }
      event.preventDefault();
      var start = area.selectionStart, end = area.selectionEnd, value = area.value;
      var lineStart = value.lastIndexOf("\n", start - 1) + 1;
      if (event.shiftKey) {
        var block = value.slice(lineStart, end);
        var outdented = block.replace(/^(\t| {1,4})/gm, "");
        area.setRangeText(outdented, lineStart, end, "preserve");
        area.selectionStart = Math.max(lineStart, start - (block.length - outdented.length > 0 ? 1 : 0));
        area.selectionEnd = end - (block.length - outdented.length);
      } else if (start !== end && value.slice(start, end).indexOf("\n") >= 0) {
        var selected = value.slice(lineStart, end);
        var indented = selected.replace(/^/gm, "\t");
        area.setRangeText(indented, lineStart, end, "preserve");
        area.selectionStart = start + 1;
        area.selectionEnd = end + (indented.length - selected.length);
      } else {
        area.setRangeText("\t", start, end, "end");
      }
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });

    // Ctrl/Cmd+Enter submits.
    form.addEventListener("keydown", function (event) {
      if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        form.requestSubmit();
      }
    });

    refresh();
  }

  document.addEventListener("DOMContentLoaded", function () {
    var form = document.querySelector("[data-gist-form]");
    if (form) setupEditor(form);
    if (document.body.getAttribute("data-page") === "new") showYours();
    else if (readYours().length) {
      var link = document.querySelector("[data-yours-link]");
      if (link) link.hidden = false;
    }
    rememberOwned();
    markTarget();
  });
})();
