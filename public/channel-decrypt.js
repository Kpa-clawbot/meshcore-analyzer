/**
 * Client-side MeshCore channel decryption module.
 *
 * Implements the same crypto as internal/channel/channel.go:
 *   - Key derivation: SHA-256("#channelname")[:16]
 *   - Channel hash: SHA-256(key)[0]
 *   - MAC: HMAC-SHA256 with 32-byte secret (key + 16 zero bytes), truncated to 2 bytes
 *   - Encryption: AES-128-ECB (block-by-block)
 *   - Plaintext: timestamp(4 LE) + flags(1) + "sender: message\0"
 *
 * Keys NEVER leave the browser. No fetch/XHR/network calls in this module.
 */
/* eslint-disable no-var */
window.ChannelDecrypt = (function () {
  'use strict';

  var STORAGE_KEY = 'corescope_channel_keys';
  var CACHE_KEY = 'corescope_channel_cache';

  // ---- Hex utilities ----

  function bytesToHex(bytes) {
    var hex = '';
    for (var i = 0; i < bytes.length; i++) {
      hex += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
    }
    return hex;
  }

  function hexToBytes(hex) {
    var bytes = new Uint8Array(hex.length / 2);
    for (var i = 0; i < hex.length; i += 2) {
      bytes[i / 2] = parseInt(hex.substring(i, i + 2), 16);
    }
    return bytes;
  }

  // ---- Key derivation ----

  /**
   * Derive AES-128 key from channel name: SHA-256("#channelname")[:16].
   * @param {string} channelName - e.g. "#LongFast"
   * @returns {Promise<Uint8Array>} 16-byte key
   */
  async function deriveKey(channelName) {
    var enc = new TextEncoder();
    var hash = await crypto.subtle.digest('SHA-256', enc.encode(channelName));
    return new Uint8Array(hash).slice(0, 16);
  }

  /**
   * Compute the 1-byte channel hash: SHA-256(key)[0].
   * @param {Uint8Array} key - 16-byte key
   * @returns {Promise<number>} single byte (0-255)
   */
  async function computeChannelHash(key) {
    var hash = await crypto.subtle.digest('SHA-256', key);
    return new Uint8Array(hash)[0];
  }

  // ---- AES-128-ECB via Web Crypto (CBC with zero IV, block-by-block) ----

  /**
   * Decrypt AES-128-ECB by decrypting each 16-byte block independently
   * using AES-CBC with a zero IV (equivalent to ECB for single blocks).
   * @param {Uint8Array} key - 16-byte AES key
   * @param {Uint8Array} ciphertext - must be multiple of 16 bytes
   * @returns {Promise<Uint8Array>} plaintext
   */
  async function decryptECB(key, ciphertext) {
    if (ciphertext.length === 0 || ciphertext.length % 16 !== 0) {
      return null;
    }
    var cryptoKey = await crypto.subtle.importKey(
      'raw', key, { name: 'AES-CBC' }, false, ['decrypt']
    );
    var zeroIV = new Uint8Array(16);
    var plaintext = new Uint8Array(ciphertext.length);

    for (var i = 0; i < ciphertext.length; i += 16) {
      var block = ciphertext.slice(i, i + 16);
      // Append a dummy block (16 bytes of 0x10 = PKCS7 padding for empty next block)
      // so Web Crypto doesn't complain about padding
      var padded = new Uint8Array(32);
      padded.set(block, 0);
      // Second block is PKCS7 padding: 16 bytes of 0x10
      for (var j = 16; j < 32; j++) padded[j] = 16;

      var decrypted = await crypto.subtle.decrypt(
        { name: 'AES-CBC', iv: zeroIV }, cryptoKey, padded
      );
      var decBytes = new Uint8Array(decrypted);
      plaintext.set(decBytes.slice(0, 16), i);
    }

    return plaintext;
  }

  // ---- MAC verification ----

  /**
   * Verify HMAC-SHA256 MAC (first 2 bytes) using 32-byte secret (key + 16 zero bytes).
   * @param {Uint8Array} key - 16-byte AES key
   * @param {Uint8Array} ciphertext - encrypted data
   * @param {string} macHex - 4-char hex string (2 bytes)
   * @returns {Promise<boolean>}
   */
  async function verifyMAC(key, ciphertext, macHex) {
    // Build 32-byte channel secret: key + 16 zero bytes
    var secret = new Uint8Array(32);
    secret.set(key, 0);
    // remaining 16 bytes are already 0

    var cryptoKey = await crypto.subtle.importKey(
      'raw', secret, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']
    );
    var sig = await crypto.subtle.sign('HMAC', cryptoKey, ciphertext);
    var sigBytes = new Uint8Array(sig);

    var macBytes = hexToBytes(macHex);
    return sigBytes[0] === macBytes[0] && sigBytes[1] === macBytes[1];
  }

  // ---- Plaintext parsing ----

  /**
   * Parse decrypted plaintext: timestamp(4 LE) + flags(1) + "sender: message\0..."
   * @param {Uint8Array} plaintext
   * @returns {{ timestamp: number, flags: number, sender: string, message: string } | null}
   */
  function parsePlaintext(plaintext) {
    if (!plaintext || plaintext.length < 5) return null;

    var timestamp = plaintext[0] | (plaintext[1] << 8) | (plaintext[2] << 16) | ((plaintext[3] << 24) >>> 0);
    var flags = plaintext[4];

    // Extract text up to first null byte
    var textBytes = plaintext.slice(5);
    var nullIdx = -1;
    for (var i = 0; i < textBytes.length; i++) {
      if (textBytes[i] === 0) { nullIdx = i; break; }
    }
    var text = new TextDecoder().decode(nullIdx >= 0 ? textBytes.slice(0, nullIdx) : textBytes);

    // Count non-printable characters
    var nonPrintable = 0;
    for (var c = 0; c < text.length; c++) {
      var code = text.charCodeAt(c);
      if (code < 32 && code !== 10 && code !== 13 && code !== 9) nonPrintable++;
    }
    if (nonPrintable > 2) return null;

    // Parse "sender: message" format
    var colonIdx = text.indexOf(': ');
    if (colonIdx > 0 && colonIdx < 50) {
      var potentialSender = text.substring(0, colonIdx);
      if (potentialSender.indexOf(':') < 0 && potentialSender.indexOf('[') < 0 && potentialSender.indexOf(']') < 0) {
        return { timestamp: timestamp, flags: flags, sender: potentialSender, message: text.substring(colonIdx + 2) };
      }
    }

    return { timestamp: timestamp, flags: flags, sender: '', message: text };
  }

  // ---- Full decrypt pipeline ----

  /**
   * Verify MAC, decrypt, and parse a single packet.
   * @param {Uint8Array} keyBytes - 16-byte key
   * @param {string} macHex - 4-char hex MAC
   * @param {string} encryptedHex - hex-encoded ciphertext
   * @returns {Promise<{ sender: string, message: string, timestamp: number } | null>}
   */
  async function decrypt(keyBytes, macHex, encryptedHex) {
    var ciphertext = hexToBytes(encryptedHex);
    if (ciphertext.length === 0 || ciphertext.length % 16 !== 0) return null;

    var macOk = await verifyMAC(keyBytes, ciphertext, macHex);
    if (!macOk) return null;

    var plaintext = await decryptECB(keyBytes, ciphertext);
    if (!plaintext) return null;

    return parsePlaintext(plaintext);
  }

  // Alias used by channels.js
  var decryptPacket = decrypt;

  // ---- Key storage (localStorage) ----

  function saveKey(channelName, keyHex) {
    var keys = getKeys();
    keys[channelName] = keyHex;
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(keys)); } catch (e) { /* quota */ }
  }

  // Alias used by channels.js
  var storeKey = saveKey;

  function getKeys() {
    try {
      var raw = localStorage.getItem(STORAGE_KEY);
      return raw ? JSON.parse(raw) : {};
    } catch (e) { return {}; }
  }

  // Alias used by channels.js
  var getStoredKeys = getKeys;

  function removeKey(channelName) {
    var keys = getKeys();
    delete keys[channelName];
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(keys)); } catch (e) { /* quota */ }
  }

  // ---- Message cache (localStorage) ----

  function cacheMessages(channelHash, messages) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      cache[channelHash] = { messages: messages, ts: Date.now() };
      localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
    } catch (e) { /* quota */ }
  }

  function getCachedMessages(channelHash) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      var entry = cache[channelHash];
      return entry ? entry.messages : null;
    } catch (e) { return null; }
  }

  // Cache with lastTimestamp (used by channels.js via getCache/setCache)
  function setCache(key, messages, lastTimestamp) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      cache[key] = { messages: messages, lastTimestamp: lastTimestamp, ts: Date.now() };
      localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
    } catch (e) { /* quota */ }
  }

  function getCache(key) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      return cache[key] || null;
    } catch (e) { return null; }
  }

  return {
    deriveKey: deriveKey,
    decrypt: decrypt,
    decryptPacket: decryptPacket,
    decryptECB: decryptECB,
    verifyMAC: verifyMAC,
    parsePlaintext: parsePlaintext,
    computeChannelHash: computeChannelHash,
    bytesToHex: bytesToHex,
    hexToBytes: hexToBytes,
    saveKey: saveKey,
    storeKey: storeKey,
    getKeys: getKeys,
    getStoredKeys: getStoredKeys,
    removeKey: removeKey,
    cacheMessages: cacheMessages,
    getCachedMessages: getCachedMessages,
    setCache: setCache,
    getCache: getCache
  };
})();
