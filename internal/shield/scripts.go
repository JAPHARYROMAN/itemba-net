package shield

// PreInjectedShieldScript is injected into every frame before any page scripts run via Page.addScriptToEvaluateOnNewDocument.
// It implements Layer 3 of itemba-shield AND real-time DOM media stream extraction:
// 1. Window-Spawn Lock (overrides window.open and blocks cross-origin target="_blank" tab-under hijacks)
// 2. Z-Index Clickjacking Sweeper (MutationObserver + geometric probe stripping invisible high-z-index overlays over <video>)
// 3. Cosmetic Ad Container Sanitizer
// 4. Active <video> / <source> DOM Media Sniffer (reports playable streams to __itembaMediaReport)
const PreInjectedShieldScript = `
(() => {
  if (window.__itembaShieldActive) return;
  window.__itembaShieldActive = true;

  const reportThreat = (layer, category, target, rule) => {
    try {
      if (typeof window.__itembaShieldReport === 'function') {
        window.__itembaShieldReport(JSON.stringify({
          layer: layer,
          category: category,
          target: String(target || '').slice(0, 220),
          rule: String(rule || '')
        }));
      }
    } catch (_) {}
  };

  const reportedStreams = new Set();
  const reportMedia = (url, format, resolution) => {
    if (!url || typeof url !== 'string') return;
    if (url.startsWith('blob:') || url.startsWith('data:')) return;
    if (reportedStreams.has(url)) return;
    reportedStreams.add(url);
    try {
      if (typeof window.__itembaMediaReport === 'function') {
        window.__itembaMediaReport(JSON.stringify({
          url: url,
          format: format || 'HTML5 <video>',
          resolution: resolution || '',
          pageUrl: window.location.href
        }));
      }
    } catch (_) {}
  };

  // --- 1. WINDOW-SPAWN LOCK (Pop-Up & Tab-Under Guillotine) ---
  const blockedOpen = function(url, target, features) {
    reportThreat(
      'L3_POPUP_GUILLOTINE',
      'window.open() Hijack Blocked',
      url || 'about:blank',
      'window.open = () => null'
    );
    return null;
  };
  try {
    Object.defineProperty(window, 'open', {
      configurable: false,
      writable: false,
      value: blockedOpen
    });
  } catch (_) {
    window.open = blockedOpen;
  }

  window.addEventListener('beforeunload', (e) => {
    e.stopImmediatePropagation();
  }, true);

  document.addEventListener('click', (e) => {
    const anchor = e.target && e.target.closest ? e.target.closest('a') : null;
    if (!anchor) return;
    const href = anchor.getAttribute('href') || '';
    const target = anchor.getAttribute('target') || '';
    if (
      target === '_blank' ||
      anchor.hasAttribute('data-popunder') ||
      href.includes('popads') ||
      href.includes('exoclick') ||
      href.includes('adsterra') ||
      href.includes('clickjack')
    ) {
      try {
        const dest = new URL(href, window.location.href);
        if (dest.origin !== window.location.origin || anchor.hasAttribute('data-popunder')) {
          e.preventDefault();
          e.stopPropagation();
          reportThreat(
            'L3_POPUP_GUILLOTINE',
            'Target-Blank Redirect Hijack',
            dest.href,
            'a[target="_blank"] origin lock'
          );
        }
      } catch (_) {}
    }
  }, true);

  // --- 2. Z-INDEX CLICKJACKING SWEEPER & COSMETIC SANITIZER ---
  const AD_SELECTORS = [
    '[id*="google_ads"]',
    '[class*="ad-overlay"]',
    '[class*="clickjack"]',
    '[id*="clickjack"]',
    '[class*="popunder"]',
    '[id*="popunder"]',
    '[data-ad-slot]',
    '[data-ad-overlay]',
    '.ad-banner',
    '.preroll-overlay',
    '.fake-play-ad',
    'iframe[src*="doubleclick"]',
    'iframe[src*="exoclick"]',
    'iframe[src*="adsterra"]'
  ];

  const sweptSet = new WeakSet();

  const inspectElement = (el) => {
    if (!el || el.nodeType !== 1 || sweptSet.has(el)) return;
    const tag = (el.tagName || '').toLowerCase();
    if (tag === 'video') {
      const src = el.currentSrc || el.src;
      if (src) {
        const res = el.videoHeight ? el.videoHeight + 'p' : '';
        reportMedia(src, 'HTML5 <video>', res);
      }
      const sources = el.querySelectorAll('source');
      for (const s of sources) {
        if (s.src) reportMedia(s.src, s.type || 'HTML5 <source>', '');
      }
      return;
    }
    if (tag === 'html' || tag === 'body' || tag === 'head' || tag === 'script' || tag === 'style') {
      return;
    }
    if (el.id === 'itemba-theater-wrapper' || el.hasAttribute('data-itemba-safe')) {
      return;
    }
    if (el.querySelector && el.querySelector('video')) {
      return;
    }

    for (const sel of AD_SELECTORS) {
      try {
        if (el.matches(sel)) {
          sweptSet.add(el);
          const desc = tag + (el.id ? '#' + el.id : '') + (el.className && typeof el.className === 'string' ? '.' + el.className.trim().replace(/\s+/g, '.') : '');
          el.remove();
          reportThreat('L3_DOM_SWEEPER', 'Cosmetic Ad Node Stripped', desc, sel);
          return;
        }
      } catch (_) {}
    }

    try {
      const style = window.getComputedStyle(el);
      const pos = style.position;
      const zIndex = parseInt(style.zIndex, 10);
      if ((pos === 'fixed' || pos === 'absolute') && !isNaN(zIndex) && zIndex >= 9000) {
        const rect = el.getBoundingClientRect();
        const coversLargeArea = rect.width >= window.innerWidth * 0.35 && rect.height >= window.innerHeight * 0.25;
        const opacity = parseFloat(style.opacity);
        const bg = style.backgroundColor || '';
        const isTransparent = opacity < 0.25 || bg === 'transparent' || bg === 'rgba(0, 0, 0, 0)' || el.hasAttribute('data-clickjack');

        let overlapsVideo = false;
        const videos = document.querySelectorAll('video');
        for (const v of videos) {
          const vr = v.getBoundingClientRect();
          if (
            vr.width > 40 &&
            rect.left < vr.right &&
            rect.right > vr.left &&
            rect.top < vr.bottom &&
            rect.bottom > vr.top
          ) {
            overlapsVideo = true;
            break;
          }
        }

        if ((coversLargeArea && isTransparent) || (overlapsVideo && zIndex >= 9999)) {
          sweptSet.add(el);
          const desc = tag + (el.id ? '#' + el.id : '') + '[z-index:' + zIndex + ']';
          el.remove();
          reportThreat(
            'L3_DOM_SWEEPER',
            'Z-Index Clickjacking Overlay Removed',
            desc,
            'MutationObserver z-index=' + zIndex
          );
        }
      }
    } catch (_) {}
  };

  const runFullSweep = () => {
    if (!document.body) return;
    const all = document.body.querySelectorAll('*');
    for (const el of all) {
      inspectElement(el);
    }
  };

  const startObserver = () => {
    runFullSweep();
    const obs = new MutationObserver((mutations) => {
      for (const m of mutations) {
        for (const node of m.addedNodes) {
          if (node.nodeType === 1) {
            inspectElement(node);
            if (node.querySelectorAll) {
              for (const child of node.querySelectorAll('*')) {
                inspectElement(child);
              }
            }
          }
        }
        if (m.type === 'attributes' && m.target && m.target.nodeType === 1) {
          inspectElement(m.target);
        }
      }
    });
    if (document.documentElement) {
      obs.observe(document.documentElement, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: ['style', 'class', 'id', 'src']
      });
    }
    setInterval(runFullSweep, 1200);
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', startObserver);
  } else {
    startObserver();
  }
})();
`

// IsolateTheaterScript locates the primary <video> or active player iframe on the page and promotes it into a clean,
// zero-distraction full-viewport cinema experience inside the sandbox.
const IsolateTheaterScript = `
(() => {
  const videos = Array.from(document.querySelectorAll('video'));
  let bestVideo = null;
  let bestArea = 0;

  for (const v of videos) {
    const r = v.getBoundingClientRect();
    const area = Math.max(r.width, 320) * Math.max(r.height, 180);
    if (area >= bestArea) {
      bestArea = area;
      bestVideo = v;
    }
  }

  if (bestVideo) {
    bestVideo.setAttribute('data-itemba-safe', 'true');
    bestVideo.controls = true;
    bestVideo.style.cssText = 'position:fixed !important; top:0 !important; left:0 !important; width:100vw !important; height:100vh !important; z-index:2147483647 !important; background:#000 !important; object-fit:contain !important; margin:0 !important; padding:0 !important; border:none !important;';
    document.documentElement.style.cssText = 'background:#000 !important; overflow:hidden !important; margin:0 !important; padding:0 !important;';
    document.body.style.cssText = 'background:#000 !important; overflow:hidden !important; margin:0 !important; padding:0 !important;';
    try {
      bestVideo.muted = false;
      bestVideo.play().catch(() => {
        bestVideo.muted = true;
        bestVideo.play().catch(() => {});
      });
    } catch (_) {}
    return { isolated: true, type: 'video', src: bestVideo.currentSrc || bestVideo.src || '' };
  }

  const iframes = Array.from(document.querySelectorAll('iframe'));
  for (const f of iframes) {
    const src = (f.src || '').toLowerCase();
    if (src && !src.includes('doubleclick') && !src.includes('ads')) {
      f.setAttribute('data-itemba-safe', 'true');
      f.style.cssText = 'position:fixed !important; top:0 !important; left:0 !important; width:100vw !important; height:100vh !important; z-index:2147483647 !important; background:#000 !important; border:none !important;';
      document.body.style.cssText = 'background:#000 !important; overflow:hidden !important;';
      return { isolated: true, type: 'iframe', src: f.src };
    }
  }

  return { isolated: false, reason: 'No active <video> or player <iframe> found on page' };
})();
`
