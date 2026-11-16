// The composer's file-admission chip. Hidden while the session is in the
// default workspace mode; in explicit mode it says so and counts what is
// admitted for editing, its tooltip lists the paths (and the read-only
// attachments), and a click runs /admit, which opens the same report the
// terminal prints. Fed by the status event (file_mode, admitted, attached).
(function (global) {
  "use strict";
  global.JanetAdmission = function ({ post }) {
    const chip = document.getElementById("files-chip");
    if (chip) chip.addEventListener("click", () => post("/api/command", { text: "/admit" }));
    function paint(st) {
      if (!chip) return;
      const explicit = st && st.file_mode === "explicit";
      chip.hidden = !explicit;
      if (!explicit) return;
      const edit = st.admitted || [];
      const attach = st.attached || [];
      chip.textContent = "Files: explicit · " + edit.length + " admitted";
      const lines = ["File mode: explicit — the model may edit only admitted files"];
      lines.push(edit.length ? "Admitted for editing: " + edit.join(", ") : "Nothing admitted yet: /admit <path>");
      if (attach.length) lines.push("Read-only context: " + attach.join(", "));
      lines.push("Click for the full list");
      chip.title = lines.join("\n");
      chip.setAttribute("aria-label", "File mode explicit, " + edit.length + " admitted for editing");
    }
    return { paint };
  };
})(window);
