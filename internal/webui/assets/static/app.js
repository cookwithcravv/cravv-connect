// cravv-connect web UI. Everything works without this script; it only asks
// before drastic actions and starts waits a page asks for.
"use strict";

document.addEventListener("submit", function (e) {
  var msg = e.target.getAttribute("data-confirm");
  if (msg && !window.confirm(msg)) {
    e.preventDefault();
  }
});

document.addEventListener("DOMContentLoaded", function () {
  var form = document.querySelector("form[data-autosubmit]");
  if (form) {
    form.submit();
  }
});
