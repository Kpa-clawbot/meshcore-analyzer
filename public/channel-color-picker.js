/**
 * Channel Color Quick-Assign Popover (M2, #271)
 *
 * Right-click (or long-press on mobile) a channel name in the live feed
 * or packets table to open a color picker popover.
 *
 * Uses ChannelColors.set/get/remove from channel-colors.js (M1).
 */
(function() {
  'use strict';

  // Curated maximally-distinct palette (10 swatches, ColorBrewer-inspired)
  var PRESET_COLORS = [
    '#ef4444', // red
    '#f97316', // orange
    '#eab308', // yellow
    '#22c55e', // green
    '#06b6d4', // cyan
    '#3b82f6', // blue
    '#8b5cf6', // violet
    '#ec4899', // pink
    '#14b8a6', // teal
    '#f43f5e'  // rose
  ];

  var popoverEl = null;
  var currentChannel = null;
  var longPressTimer = null;

  function createPopover() {
    if (popoverEl) return popoverEl;
    var el = document.createElement('div');
    el.className = 'cc-picker-popover';
    el.setAttribute('role', 'dialog');
    el.setAttribute('aria-label', 'Channel color picker');
    el.style.display = 'none';
    el.innerHTML =
      '<div class="cc-picker-header">' +
        '<span class="cc-picker-title" id="cc-picker-title"></span>' +
        '<button class="cc-picker-close" title="Close" aria-label="Close">✕</button>' +
      '</div>' +
      '<div class="cc-picker-swatches" role="group" aria-label="Color swatches"></div>' +
      '<div class="cc-picker-custom">' +
        '<label>Custom: <input type="color" class="cc-picker-input" value="#3b82f6" aria-label="Custom color"></label>' +
        '<button class="cc-picker-apply">Apply</button>' +
      '</div>' +
      '<button class="cc-picker-clear">Clear color</button>';
    el.setAttribute('aria-labelledby', 'cc-picker-title');

    // Build swatches
    var swatchContainer = el.querySelector('.cc-picker-swatches');
    for (var i = 0; i < PRESET_COLORS.length; i++) {
      var sw = document.createElement('button');
      sw.className = 'cc-swatch';
      sw.style.background = PRESET_COLORS[i];
      sw.setAttribute('data-color', PRESET_COLORS[i]);
      sw.setAttribute('aria-label', PRESET_COLORS[i]);
      sw.title = PRESET_COLORS[i];
      swatchContainer.appendChild(sw);
    }

    // Event: swatch click
    swatchContainer.addEventListener('click', function(e) {
      var btn = e.target.closest('.cc-swatch');
      if (!btn) return;
      assignColor(btn.getAttribute('data-color'));
    });

    // Keyboard navigation for swatches (arrow keys)
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

    // Event: custom apply
    el.querySelector('.cc-picker-apply').addEventListener('click', function() {
      var input = el.querySelector('.cc-picker-input');
      assignColor(input.value);
    });

    // Event: clear
    el.querySelector('.cc-picker-clear').addEventListener('click', function() {
      if (currentChannel && window.ChannelColors) {
        window.ChannelColors.remove(currentChannel);
        refreshVisibleRows();
      }
      hidePopover();
    });

    // Event: close button
    el.querySelector('.cc-picker-close').addEventListener('click', function() {
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

    // Update title
    el.querySelector('.cc-picker-title').textContent = channel;

    // Highlight current color
    var current = window.ChannelColors ? window.ChannelColors.get(channel) : null;
    var swatches = el.querySelectorAll('.cc-swatch');
    for (var i = 0; i < swatches.length; i++) {
      swatches[i].classList.toggle('cc-swatch-active', swatches[i].getAttribute('data-color') === current);
    }
    if (current) {
      el.querySelector('.cc-picker-input').value = current;
    }

    // Show/hide clear button
    el.querySelector('.cc-picker-clear').style.display = current ? '' : 'none';

    // Position — on touch devices, CSS handles bottom-sheet via @media(pointer:coarse)
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
      var focusable = popoverEl.querySelectorAll('button, input, [tabindex]');
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

  /** Refresh channel color styles on all visible feed items and packet rows. */
  function refreshVisibleRows() {
    if (!window.ChannelColors) return;

    // Live feed items
    var feedItems = document.querySelectorAll('.live-feed-item');
    for (var i = 0; i < feedItems.length; i++) {
      var item = feedItems[i];
      var ch = item._ccChannel;
      if (!ch) continue;
      var style = window.ChannelColors.getRowStyle('GRP_TXT', ch);
      // Remove old channel color styles, reapply
      item.style.borderLeft = '';
      item.style.background = '';
      if (style) item.style.cssText += style;
    }

    // Packets table — trigger re-render via custom event
    document.dispatchEvent(new CustomEvent('channel-colors-changed'));
  }

  /**
   * Extract channel name from a packet object.
   * Returns null if no channel found or not a GRP_TXT/CHAN type.
   */
  function extractChannel(pkt) {
    if (!pkt) return null;
    var d = pkt.decoded || {};
    var h = d.header || {};
    var p = d.payload || {};
    var type = h.payloadTypeName || '';
    if (type !== 'GRP_TXT' && type !== 'CHAN') return null;
    return p.channelName || null;
  }

  /**
   * Extract channel from a packets-table decoded_json.
   */
  function extractChannelFromDecoded(decoded) {
    if (!decoded) return null;
    var type = decoded.type || '';
    if (type !== 'GRP_TXT' && type !== 'CHAN') return null;
    return decoded.channel || null;
  }

  /**
   * Install context-menu (right-click) and long-press handlers on the live feed.
   */
  function installLiveFeedHandlers() {
    var feed = document.getElementById('liveFeed');
    if (!feed) return;

    feed.addEventListener('contextmenu', function(e) {
      var item = e.target.closest('.live-feed-item');
      if (!item || !item._ccChannel) return;
      var ch = item._ccChannel;
      e.preventDefault();
      showPopover(ch, e.clientX, e.clientY);
    });

    // Long-press for mobile
    var longPressTriggered = false;
    feed.addEventListener('touchstart', function(e) {
      var item = e.target.closest('.live-feed-item');
      if (!item || !item._ccChannel) return;
      var ch = item._ccChannel;
      if (!ch) return;
      var touch = e.touches[0];
      var tx = touch.clientX;
      var ty = touch.clientY;
      longPressTriggered = false;
      // Don't preventDefault here — it blocks scroll initiation on feed items.
      // CSS -webkit-touch-callout:none + user-select:none (on .live-feed-item)
      // already suppress native context menu and text selection.
      longPressTimer = setTimeout(function() {
        longPressTimer = null;
        longPressTriggered = true;
        showPopover(ch, tx, ty);
      }, 500);
    }, { passive: true });

    feed.addEventListener('touchend', function(e) {
      if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
      if (longPressTriggered) { e.preventDefault(); longPressTriggered = false; }
    });
    feed.addEventListener('touchmove', function() {
      if (longPressTimer) { clearTimeout(longPressTimer); longPressTimer = null; }
    });
    // Prevent context menu on long-press (some browsers fire contextmenu after touch)
    feed.addEventListener('contextmenu', function(e) {
      if (longPressTriggered) e.preventDefault();
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
      // Try to get decoded data from the row's data attribute
      var decodedStr = row.getAttribute('data-decoded');
      var decoded = null;
      if (decodedStr) {
        try { decoded = JSON.parse(decodedStr); } catch(ex) {}
      }
      // Fallback: check if the row has a chan-tag
      if (!decoded) {
        var chanTag = row.querySelector('.chan-tag');
        if (chanTag) {
          var ch = chanTag.textContent.trim();
          if (ch) {
            e.preventDefault();
            showPopover(ch, e.clientX, e.clientY);
            return;
          }
        }
        return;
      }
      var ch = extractChannelFromDecoded(decoded);
      if (!ch) return;
      e.preventDefault();
      showPopover(ch, e.clientX, e.clientY);
    });
  }

  // Export for use by live.js feed item creation
  window.ChannelColorPicker = {
    install: function() {
      installLiveFeedHandlers();
      installPacketsTableHandlers();
    },
    installLiveFeed: installLiveFeedHandlers,
    installPacketsTable: installPacketsTableHandlers,
    show: showPopover,
    hide: hidePopover
  };
})();
