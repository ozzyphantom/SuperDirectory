/* SuperDirectory site script: the only JavaScript on this site.
 *
 * Three jobs, and no others:
 *   1. Before first paint, stamp <html> with the reader's platform
 *      (data-os), the install tab to show (data-install), and the class "js".
 *      CSS draws key names, the visible install panel and the selected tab
 *      from these, so none of them is ever briefly wrong (UX standard [13.5]).
 *   2. The install tabs: the ARIA tabs pattern, with arrow keys, Home and
 *      End (UX standard [06.1]).
 *   3. The copy buttons on the install commands.
 *
 * Loaded blocking, first in <head> (src/layouts/Base.astro), because job 1
 * must run before the body paints. The CSP in public/_headers allows no
 * inline script, so this is a same-origin file. Jobs 2 and 3 wait for the
 * DOM.
 *
 * ES5 and feature-tested, so an old browser parses it and skips what it
 * cannot do. If this file is blocked or fails, the page still works: the
 * three install methods show as plain sections and no dead button appears.
 * Non-ASCII characters are written as escapes, so the file does not depend
 * on the charset it is served with.
 */
(function () {
  'use strict';

  var root = document.documentElement;
  var STORE = 'sd-install';

  /* Without these, the tabs cannot be wired: leave the no-script page. */
  if (!document.querySelector || !document.addEventListener) return;

  /* ---- 1. platform, before first paint --------------------------------- */

  function detectOS() {
    var nav = window.navigator || {};
    var hint = '';
    try {
      if (nav.userAgentData && nav.userAgentData.platform) hint = nav.userAgentData.platform;
    } catch (e) { /* client hints unavailable */ }
    var s = ((hint || nav.platform || '') + ' ' + (nav.userAgent || '')).toLowerCase();
    /* Phones and tablets first: Android reports Linux, and iPadOS asks for
       the desktop site and reports a Mac. Neither runs a terminal program. */
    if (/android|iphone|ipad|ipod/.test(s)) return 'mobile';
    if (/mac/.test(s)) return nav.maxTouchPoints > 1 ? 'mobile' : 'mac';
    if (/win/.test(s)) return 'windows';
    if (/linux|x11|cros|bsd/.test(s)) return 'linux';
    return 'other';
  }

  function isTab(name) {
    return name === 'brew' || name === 'download' || name === 'go';
  }

  var os = detectOS();
  var saved = null;
  try { saved = window.sessionStorage.getItem(STORE); } catch (e) { /* storage blocked */ }

  root.setAttribute('data-os', os);
  /* A choice made in this browser tab wins (UX standard [08.1]); otherwise
     Homebrew on a Mac, the download on Windows and Linux. */
  root.setAttribute('data-install', isTab(saved) ? saved
    : (os === 'windows' || os === 'linux') ? 'download' : 'brew');
  root.className += (root.className ? ' ' : '') + 'js';

  /* ---- 2. install tabs ------------------------------------------------- */

  function wireTabs() {
    var list = document.querySelector('[data-tabs]');
    if (!list) return;
    var tabs = list.querySelectorAll('[data-tab]');
    var i;

    list.setAttribute('role', 'tablist');
    list.setAttribute('aria-label', list.getAttribute('data-tabs'));
    for (i = 0; i < tabs.length; i++) {
      var name = tabs[i].getAttribute('data-tab');
      var panel = document.getElementById('install-' + name);
      tabs[i].id = 'tab-' + name;
      tabs[i].setAttribute('role', 'tab');
      tabs[i].setAttribute('aria-controls', panel.id);
      panel.setAttribute('role', 'tabpanel');
      panel.setAttribute('aria-labelledby', tabs[i].id);
      panel.setAttribute('tabindex', '0');
      tabs[i].addEventListener('click', onClick);
      tabs[i].addEventListener('keydown', onKeydown);
    }
    show(root.getAttribute('data-install'), false);

    function show(name, focus) {
      root.setAttribute('data-install', name);
      for (var j = 0; j < tabs.length; j++) {
        var on = tabs[j].getAttribute('data-tab') === name;
        tabs[j].setAttribute('aria-selected', on ? 'true' : 'false');
        tabs[j].setAttribute('tabindex', on ? '0' : '-1');
        if (on && focus) tabs[j].focus();
      }
    }

    function choose(name, focus) {
      show(name, focus);
      try { window.sessionStorage.setItem(STORE, name); } catch (e) { /* storage blocked */ }
    }

    function onClick() {
      choose(this.getAttribute('data-tab'), false);
    }

    function onKeydown(e) {
      var at = 0;
      for (var j = 0; j < tabs.length; j++) if (tabs[j] === this) at = j;
      var key = e.key;
      var to;
      if (key === 'ArrowRight' || key === 'Right') to = (at + 1) % tabs.length;
      else if (key === 'ArrowLeft' || key === 'Left') to = (at - 1 + tabs.length) % tabs.length;
      else if (key === 'Home') to = 0;
      else if (key === 'End') to = tabs.length - 1;
      else return;
      e.preventDefault();
      choose(tabs[to].getAttribute('data-tab'), true);
    }
  }

  /* ---- 3. copy buttons ------------------------------------------------- */

  function wireCopy() {
    var status = document.querySelector('[data-copy-status]');
    var buttons = document.querySelectorAll('[data-copy]');
    for (var i = 0; i < buttons.length; i++) buttons[i].addEventListener('click', onCopy);

    function onCopy() {
      var button = this;
      var text = button.getAttribute('data-copy');
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(
          function () { report(button, true); },
          function () { report(button, selectAndCopy(button)); }
        );
      } else {
        report(button, selectAndCopy(button));
      }
    }

    /* The fallback: select the command, then try the older copy command. If
       that fails too, the command stays selected for the reader to copy. */
    function selectAndCopy(button) {
      var code = button.parentNode.querySelector('[data-copy-text]');
      var selection = window.getSelection && window.getSelection();
      if (!code || !selection || !document.createRange) return false;
      var range = document.createRange();
      range.selectNodeContents(code);
      selection.removeAllRanges();
      selection.addRange(range);
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
      if (ok) selection.removeAllRanges();
      return ok;
    }

    function report(button, ok) {
      var label = button.querySelector('[data-copy-label]');
      clearTimeout(button.sdTimer);
      if (ok) {
        label.textContent = 'Copied';
        button.setAttribute('data-state', 'copied');
        say('Copied to the clipboard.', false);
      } else {
        /* Command-C on a Mac, Ctrl+C elsewhere: this is the browser's copy
           key, not the terminal's (UX standard [13.5]). A phone has no copy
           key, only the selection menu. */
        say('The browser blocked the copy. The command is selected. ' +
          (os === 'mobile' ? 'Copy it from the selection menu.'
            : 'Press ' + (os === 'mac' ? '⌘C' : 'Ctrl+C') + ' to copy it.'), true);
      }
      button.sdTimer = setTimeout(function () {
        label.textContent = 'Copy';
        button.removeAttribute('data-state');
        say('', false);
      }, ok ? 2000 : 8000);
    }

    /* The status line is a live region. A success is announced but not
       shown (the button already says Copied); a failure is shown too. */
    function say(message, visible) {
      if (!status) return;
      status.className = visible ? 'copy-status' : 'copy-status vh';
      status.textContent = '';
      if (message) setTimeout(function () { status.textContent = message; }, 50);
    }
  }

  function ready() {
    try {
      wireTabs();
      wireCopy();
    } catch (e) {
      /* Half-wired tabs are worse than none: fall back to the no-script
         page, where every install method shows. */
      root.className = root.className.replace(/(^|\s)js(\s|$)/, ' ');
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', ready);
  else ready();
})();
