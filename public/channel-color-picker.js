/**
 * Channel Color Picker — Simplified popover with 8-color constrained palette (#674)
 *
 * Click a color dot next to channel names (channels page, live feed) to open picker.
 * Right-click on live feed items retained as power-user shortcut (desktop only).
 * No long-press. No custom color input. 8 preset colors.
 *
 * Uses ChannelColors.set/get/remove from channel-colors.js.
 */
(function() {
  'use strict';

  // 8 maximally-distinct colors on dark backgrounds (#674 Tufte spec)
  var CHANNEL_PALETTE = [
    '#ef4444', // red
    '#f97316', // orange
    '#eab308', // yellow
    '#22c55e', // green
    '#06b6d4', // cyan
    '#3b82f6', // blue
    '#8b5cf6', // violet
    '#ec4899'  // pink
  ];

  var popoverEl = null;
  var currentChannel = null;

  function createPopover() {
    if (popoverEl) return popoverEl;
    var el = document.createElement('div');
    el.className = 'cc-picker-popover';
    el.setAttribute('role', 'dialog');
    el.setAttribute('aria-label', 'Channel color picker');
    el.style.display = 'none';
    el.innerHTML =
      '<div class="cc-picker-swatches" role="group" aria-label="Color swatches"></div>' +
      '<button class="cc-picker-clear">Clear color</button>';

    // Build swatches
    var swatchContainer = el.querySelector('.cc-picker-swatches');
    for (var i = 0; i < CHANNEL_PALETTE.length; i++) {
      var sw = document.createElement('button');
      sw.className = 'cc-swatch';
      sw.style.background = CHANNEL_PALETTE[i];
      sw.setAttribute('data-color', CHANNEL_PALETTE[i]);
      sw.setAttribute('aria-label', CHANNEL_PALETTE[i]);
      sw.title = CHANNEL_PALETTE[i];
      sw.setAttribute('tabindex', '0');
      swatchContainer.appendChild(sw);
    }

    // Event: swatch click
    swatchContainer.addEventListener('click', function(e) {
      var btn = e.target.closest('.cc-swatch');
      if (!btn) return;
      assignColor(btn.getAttribute('data-color'));
    });

    // Keyboard navigation for swatches
    swatchContainer.addEventListener('keydown', function(e) {
      var btn = e.target.closest('.cc-swatch');
      if (!btn) return;
      var swatches = swatchContainer.querySelectorAll('.cc-swatch');
      var idx = Array.prototype.indexOf.call(swatches, btn);
      if (idx < 0) return;
      var next = -1;
      if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (idx + 1) % swatches.length;
      else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = (idx - 1 + swatches.length) % swatches.length;
      else if (e.key === 'Enter' || e.key === ' ') { assignColor(btn.getAttribute('data-color')); e.preventDefault(); return; }
      if (next >= 0) { swatches[next].focus(); e.preventDefault(); }
    });

    // Event: clear
    el.querySelector('.cc-picker-clear').addEventListener('click', function() {
      if (currentChannel && window.ChannelColors) {
        window.ChannelColors.remove(currentChannel);
        refreshVisibleRows();
      }
      hidePopover();
    });

    // Prevent right-click on the popover itself
    el.addEventListener('contextmenu', function(e) { e.preventDefault(); });

    document.body.appendChild(el);
    popoverEl = el;
    return el;
  }

  function assignColor(color) {
    if (currentChannel && window.ChannelColors) {
      window.ChannelColors.set(currentChannel, color);
      refreshVisibleRows();
    }
    hidePopover();
  }

  function showPopover(channel, x, y) {
    var el = createPopover();
    currentChannel = channel;

    // Highlight current color
    var current = window.ChannelColors ? window.ChannelColors.get(channel) : null;
    var swatches = el.querySelectorAll('.cc-swatch');
    for (var i = 0; i < swatches.length; i++) {
      swatches[i].classList.toggle('cc-swatch-active', swatches[i].getAttribute('data-color') === current);
    }

    // Show/hide clear button
    el.querySelector('.cc-picker-clear').style.display = current ? '' : 'none';

    // Position
    el.style.display = '';
    var isTouch = window.matchMedia('(pointer: coarse)').matches;
    if (!isTouch) {
      el.style.left = '0';
      el.style.top = '0';
      var rect = el.getBoundingClientRect();
      var pw = rect.width;
      var ph = rect.height;
      var vw = window.innerWidth;
      var vh = window.innerHeight;
      var finalX = x + pw > vw ? Math.max(0, vw - pw - 8) : x;
      var finalY = y + ph > vh ? Math.max(0, vh - ph - 8) : y;
      el.style.left = finalX + 'px';
      el.style.top = finalY + 'px';
    }

    // Lock background scroll while popover is open
    document.body.style.overflow = 'hidden';

    // Focus first swatch for keyboard accessibility
    var firstSwatch = el.querySelector('.cc-swatch');
    if (firstSwatch) setTimeout(function() { firstSwatch.focus(); }, 0);

    // Listen for outside click / Escape
    setTimeout(function() {
      document.addEventListener('click', onOutsideClick, true);
      document.addEventListener('keydown', onEscape, true);
    }, 0);
  }

  function hidePopover() {
    if (popoverEl) popoverEl.style.display = 'none';
    currentChannel = null;
    document.body.style.overflow = '';
    document.removeEventListener('click', onOutsideClick, true);
    document.removeEventListener('keydown', onEscape, true);
  }

  function onOutsideClick(e) {
    if (popoverEl && !popoverEl.contains(e.target)) {
      hidePopover();
    }
  }

  function onEscape(e) {
    if (e.key === 'Escape') {
      hidePopover();
      e.stopPropagation();
    }
    // Trap Tab within the popover
    if (e.key === 'Tab' && popoverEl && popoverEl.style.display !== 'none') {
      var focusable = popoverEl.querySelectorAll('button, [tabindex]');
      if (focusable.length === 0) return;
      var first = focusable[0];
      var last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        last.focus(); e.preventDefault();
      } else if (!e.shiftKey && document.activeElement === last) {
        first.focus(); e.preventDefault();
      }
    }
  }

  /** Refresh channel color styles on all visible feed items, channel list, and packet rows. */
  function refreshVisibleRows() {
    if (!window.ChannelColors) return;

    // Live feed items
    var feedItems = document.querySelectorAll('.live-feed-item');
    for (var i = 0; i < feedItems.length; i++) {
      var item = feedItems[i];
      var ch = item._ccChannel;
      if (!ch) continue;
      var color = window.ChannelColors.get(ch);
      item.style.borderLeft = color ? '3px solid ' + color : '';
    }

    // Update color dots everywhere
    var dots = document.querySelectorAll('.ch-color-dot');
    for (var j = 0; j < dots.length; j++) {
      var dot = dots[j];
      var dotCh = dot.getAttribute('data-channel');
      if (!dotCh) continue;
      var dotColor = window.ChannelColors.get(dotCh);
      dot.style.background = dotColor || '';
    }

    // Channel list items — update border
    var chItems = document.querySelectorAll('.ch-item[data-hash]');
    for (var k = 0; k < chItems.length; k++) {
      var chItem = chItems[k];
      var hash = chItem.getAttribute('data-hash');
      if (!hash) continue;
      var chColor = window.ChannelColors.get(hash);
      chItem.style.borderLeft = chColor ? '3px solid ' + chColor : '';
    }

    // Packets table — trigger re-render via custom event
    document.dispatchEvent(new CustomEvent('channel-colors-changed'));
  }

  /**
   * Install context-menu (right-click) handler on the live feed.
   * No long-press — color dots handle mobile interaction.
   */
  function installLiveFeedHandlers() {
    var feed = document.getElementById('liveFeed');
    if (!feed) return;

    feed.addEventListener('contextmenu', function(e) {
      var item = e.target.closest('.live-feed-item');
      if (!item || !item._ccChannel) return;
      e.preventDefault();
      showPopover(item._ccChannel, e.clientX, e.clientY);
    });
  }

  /**
   * Install context-menu handler on the packets table.
   */
  function installPacketsTableHandlers() {
    var table = document.getElementById('packetsTableBody');
    if (!table) return;

    table.addEventListener('contextmenu', function(e) {
      var row = e.target.closest('tr');
      if (!row) return;
      var chanTag = row.querySelector('.chan-tag');
      if (chanTag) {
        var ch = chanTag.textContent.trim();
        if (ch) {
          e.preventDefault();
          showPopover(ch, e.clientX, e.clientY);
          return;
        }
      }
    });
  }

  // Export
  window.ChannelColorPicker = {
    install: function() {
      installLiveFeedHandlers();
      installPacketsTableHandlers();
    },
    installLiveFeed: installLiveFeedHandlers,
    installPacketsTable: installPacketsTableHandlers,
    show: showPopover,
    hide: hidePopover,
    PALETTE: CHANNEL_PALETTE
  };
})();
