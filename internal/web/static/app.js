// JavaScript น้อยที่สุด: ยืนยันก่อนลบ, ปุ่มคัดลอก, กันกดซ้ำตอนงานที่ใช้เวลานาน
(function () {
  'use strict';

  document.addEventListener('submit', function (e) {
    var f = e.target;
    if (!(f instanceof HTMLFormElement)) return;
    var msg = f.getAttribute('data-confirm');
    if (msg && !window.confirm(msg)) {
      e.preventDefault();
      return;
    }
    var b = f.querySelector('[data-busy]');
    if (b) {
      setTimeout(function () {
        b.setAttribute('data-orig', b.textContent);
        b.textContent = b.getAttribute('data-busy');
        b.disabled = true;
      }, 0);
    }
  });

  // กลับมาด้วยปุ่มย้อนกลับ: คืนค่าปุ่มที่ค้างสถานะ "กำลังทำงาน"
  window.addEventListener('pageshow', function () {
    document.querySelectorAll('[data-orig]').forEach(function (b) {
      b.textContent = b.getAttribute('data-orig');
      b.disabled = false;
    });
  });

  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.className = 'offscreen';
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (err) { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }

  document.addEventListener('click', function (e) {
    var btn = e.target.closest ? e.target.closest('[data-copy]') : null;
    if (!btn) return;
    var src = document.getElementById(btn.getAttribute('data-copy'));
    if (!src) return;
    var text = src.textContent.trim();
    var done = function (ok) {
      var old = btn.getAttribute('data-label') || btn.textContent;
      btn.setAttribute('data-label', old);
      btn.textContent = ok ? 'คัดลอกแล้ว ✓' : 'คัดลอกไม่ได้ ให้เลือกข้อความเอง';
      setTimeout(function () { btn.textContent = old; }, 2000);
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(fallbackCopy(text)); });
    } else {
      done(fallbackCopy(text));
    }
  });
})();
