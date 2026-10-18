package console

import (
	"fmt"
	"html"
	"net/http"
)

func registerTestbenchRoutes(mux *http.ServeMux) {
	// Simulated HLS manifest so itemba-scout's network sniffer verifies HLS .m3u8 stream capability
	mux.HandleFunc("/testbench/hls/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = fmt.Fprintln(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-STREAM-INF:BANDWIDTH=4500000,RESOLUTION=1920x1080\nchunklist_1080p.m3u8")
	})

	// Clean Open Cinema Stream Page
	mux.HandleFunc("/testbench/clean-cinema", func(w http.ResponseWriter, r *http.Request) {
		title := html.EscapeString(r.URL.Query().Get("q"))
		if title == "" {
			title = "ITEMBA Open Cinema Stream"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, cleanCinemaHTML, title, title, title)
	})

	// Sketchy Ad-Riddled Streaming Site Testbench (Tests all 3 layers of itemba-shield)
	mux.HandleFunc("/testbench/sketchy-cinema", func(w http.ResponseWriter, r *http.Request) {
		title := html.EscapeString(r.URL.Query().Get("q"))
		if title == "" {
			title = "SketchyStreamz Free Movie"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, sketchyCinemaHTML, title, title, title)
	})

	// Deceptive SEO Spam / Fake Registration Wall Page (Pruned by itemba-scout)
	mux.HandleFunc("/testbench/fake-wall", func(w http.ResponseWriter, r *http.Request) {
		title := html.EscapeString(r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Watch %s Full 4K</title></head>
<body style="background:#111;color:#eee;font-family:sans-serif;text-align:center;padding:80px;">
  <div data-fake-play-wall="true">
    <h1>ACCOUNT REGISTRATION & CREDIT CARD REQUIRED TO WATCH %s</h1>
    <p>Click the fake play button below to register before streaming.</p>
    <button style="padding:18px 36px;background:#e11d48;color:#fff;border:none;border-radius:8px;font-size:20px;">▶ FAKE PLAY BUTTON</button>
  </div>
</body>
</html>`, title, title)
	})
}

const cleanCinemaHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <title>%s — Open Cinema HD</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background: #07090e;
      color: #e2e8f0;
      font-family: system-ui, -apple-system, sans-serif;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      padding: 24px;
    }
    .header {
      width: 100%%;
      max-width: 1024px;
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 14px;
      padding: 12px 18px;
      background: rgba(16, 185, 129, 0.1);
      border: 1px solid rgba(16, 185, 129, 0.35);
      border-radius: 10px;
    }
    .badge {
      background: #10b981;
      color: #04130d;
      font-weight: 700;
      font-size: 12px;
      padding: 4px 10px;
      border-radius: 999px;
      text-transform: uppercase;
      letter-spacing: 0.06em;
    }
    .player-shell {
      width: 100%%;
      max-width: 1024px;
      aspect-ratio: 16 / 9;
      background: #000;
      border-radius: 12px;
      overflow: hidden;
      border: 1px solid #1e293b;
      box-shadow: 0 20px 50px rgba(0,0,0,0.75);
      position: relative;
    }
    video {
      width: 100%%;
      height: 100%%;
      object-fit: cover;
      display: block;
    }
  </style>
</head>
<body>
  <div class="header">
    <div>
      <strong style="font-size:18px;">%s</strong>
      <div style="font-size:12px;color:#94a3b8;margin-top:2px;">Verified Public Domain Catalog • Zero Trackers • 1080p60 Stream</div>
    </div>
    <span class="badge">Clean Stream • 100%% Score</span>
  </div>
  <div class="player-shell">
    <video id="primary-video" controls autoplay muted playsinline></video>
  </div>
  <script>
    // Request HLS manifest for verifier detection
    fetch('/testbench/hls/master.m3u8').catch(() => {});

    // Render a live 60fps procedural cinema broadcast into the <video> element so it plays immediately offline or online
    const canvas = document.createElement('canvas');
    canvas.width = 1280;
    canvas.height = 720;
    const ctx = canvas.getContext('2d');
    const video = document.getElementById('primary-video');
    const titleText = %q;

    let frame = 0;
    function drawCinemaFrame() {
      frame++;
      const t = frame * 0.025;
      const grad = ctx.createLinearGradient(0, 0, 1280, 720);
      grad.addColorStop(0, '#040d1a');
      grad.addColorStop(0.5, '#0b1e36');
      grad.addColorStop(1, '#051329');
      ctx.fillStyle = grad;
      ctx.fillRect(0, 0, 1280, 720);

      // Animated synthwave horizon grid
      ctx.strokeStyle = 'rgba(16, 185, 129, 0.28)';
      ctx.lineWidth = 1.5;
      for (let i = 0; i < 18; i++) {
        const y = 380 + ((i * 22 + (frame * 2) %% 22));
        if (y < 720) {
          ctx.beginPath();
          ctx.moveTo(0, y);
          ctx.lineTo(1280, y);
          ctx.stroke();
        }
      }
      for (let x = -640; x <= 1920; x += 120) {
        ctx.beginPath();
        ctx.moveTo(640 + (x - 640) * 0.2, 380);
        ctx.lineTo(x, 720);
        ctx.stroke();
      }

      // Orbiting telemetry rings
      ctx.save();
      ctx.translate(640, 250);
      ctx.strokeStyle = '#38bdf8';
      ctx.lineWidth = 3;
      ctx.beginPath();
      ctx.arc(0, 0, 95 + Math.sin(t) * 8, t, t + Math.PI * 1.35);
      ctx.stroke();

      ctx.strokeStyle = '#10b981';
      ctx.beginPath();
      ctx.arc(0, 0, 68, -t * 1.4, -t * 1.4 + Math.PI * 1.5);
      ctx.stroke();
      ctx.restore();

      // Title & timecode HUD
      ctx.fillStyle = '#f8fafc';
      ctx.font = 'bold 34px system-ui, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(titleText.toUpperCase(), 640, 110);

      ctx.fillStyle = '#38bdf8';
      ctx.font = '600 16px monospace';
      const sec = Math.floor(frame / 30);
      const mm = String(Math.floor(sec / 60)).padStart(2, '0');
      const ss = String(sec %% 60).padStart(2, '0');
      const ff = String(frame %% 30).padStart(2, '0');
      ctx.fillText('ITEMBA-VAULT ISOLATED MEDIA PIPELINE  •  TC 00:' + mm + ':' + ss + ':' + ff + '  •  1080p H.264/WebRTC', 640, 675);

      requestAnimationFrame(drawCinemaFrame);
    }
    drawCinemaFrame();

    if (canvas.captureStream) {
      video.srcObject = canvas.captureStream(30);
      video.play().catch(() => {});
    }
  </script>
</body>
</html>`

const sketchyCinemaHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <title>Watch %s FREE HD — SketchyStreamz</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background: #090b10;
      color: #f1f5f9;
      font-family: system-ui, -apple-system, sans-serif;
      padding: 20px;
    }
    .top-bar {
      max-width: 1040px;
      margin: 0 auto 14px auto;
      padding: 12px 16px;
      background: #161b26;
      border: 1px solid #334155;
      border-radius: 10px;
      display: flex;
      justify-content: space-between;
      align-items: center;
    }
    .stage {
      max-width: 1040px;
      margin: 0 auto;
      aspect-ratio: 16 / 9;
      background: #000;
      border-radius: 12px;
      overflow: hidden;
      position: relative;
      border: 1px solid #334155;
    }
    video {
      width: 100%%;
      height: 100%%;
      object-fit: cover;
      display: block;
    }
    .controls-bar {
      max-width: 1040px;
      margin: 14px auto 0 auto;
      display: flex;
      gap: 12px;
      align-items: center;
    }
    .btn {
      padding: 10px 16px;
      border-radius: 8px;
      border: 1px solid #475569;
      background: #1e293b;
      color: #f8fafc;
      cursor: pointer;
      font-weight: 600;
      font-size: 13px;
    }
    .btn:hover { background: #334155; }
  </style>
</head>
<body>
  <!-- LAYER 3 TARGET: Cosmetic Ad Banners that itemba-shield strips -->
  <div id="google_ads_frame_1" class="ad-banner" data-ad-slot="top-leaderboard" style="background:#dc2626;color:#fff;padding:12px;text-align:center;font-weight:bold;">
    [UNBLOCKED AD BANNER] HOT SINGLES IN YOUR AREA — CLICK HERE TO CLAIM PRIZE!
  </div>

  <!-- LAYER 3 TARGET: Invisible Z-Index 999999 Clickjacking Overlay covering the entire viewport -->
  <div id="clickjack-overlay-trap" data-clickjack="true" style="position:fixed;top:0;left:0;width:100vw;height:100vh;z-index:999999;background:transparent;opacity:0.01;cursor:pointer;"></div>

  <div class="top-bar">
    <div>
      <strong style="font-size:18px;color:#38bdf8;">SketchyStreamz Mirror: %s</strong>
      <div style="font-size:12px;color:#94a3b8;">Untrusted Third-Party Host • Active Sanitization by ITEMBA-SHIELD</div>
    </div>
    <div style="font-size:12px;background:#0f172a;padding:6px 12px;border-radius:6px;border:1px solid #10b981;color:#10b981;">
      ✓ Shield Protected Sandbox
    </div>
  </div>

  <div class="stage">
    <video id="sketchy-video" controls autoplay muted playsinline></video>
  </div>

  <div class="controls-bar">
    <button class="btn" id="trigger-attack-btn" onclick="triggerHostileAdWave()">
      ⚡ Simulate Hostile Ad / Pop-Up Wave Inside Page
    </button>
    <a href="https://adsterra.com/malicious-tabunder" target="_blank" data-popunder="true" class="btn" style="text-decoration:none;">
      🔗 Test Target-Blank Hijack Link
    </a>
  </div>

  <script>
    // Request valid HLS manifest so itemba-scout verifies the stream
    fetch('/testbench/hls/master.m3u8').catch(() => {});

    function triggerHostileAdWave() {
      // 1. Layer 1 DNS Sinkhole Triggers (OISD / HaGeZi / StevenBlack domains)
      const dnsTargets = [
        'https://ad.doubleclick.net/ddm/adj/sketchy.preroll',
        'https://cdn.popads.net/popunder_v4.js',
        'https://main.exosrv.com/syndication/vast.js',
        'https://sb.scorecardresearch.com/beacon.gif'
      ];
      for (const u of dnsTargets) {
        fetch(u, { mode: 'no-cors' }).catch(() => {});
      }

      // 2. Layer 2 CDP Filter Triggers (First-party EasyList / EasyPrivacy / uBlock rules)
      const cdpTargets = [
        '/ads/preroll_vast.xml?ad_tag=1',
        '/ads/popunder.js',
        '/telemetry/pixel.gif?session=track'
      ];
      for (const u of cdpTargets) {
        fetch(u).catch(() => {});
      }

      // 3. Layer 3 Window-Spawn Lock Trigger (window.open hijack attempt)
      try {
        window.open('https://popcash.net/scam-redirect', '_blank');
      } catch (_) {}

      // 4. Layer 3 Z-Index Clickjacking Overlay Mutation
      const rogueOverlay = document.createElement('div');
      rogueOverlay.id = 'rogue-clickjack-' + Date.now();
      rogueOverlay.setAttribute('data-clickjack', 'true');
      rogueOverlay.style.cssText = 'position:fixed;top:0;left:0;width:100vw;height:100vh;z-index:9999999;opacity:0.01;background:transparent;';
      document.body.appendChild(rogueOverlay);
    }

    // Fire an initial hostile wave on page boot
    setTimeout(triggerHostileAdWave, 150);

    // Drive the live 60fps video player
    const canvas = document.createElement('canvas');
    canvas.width = 1280;
    canvas.height = 720;
    const ctx = canvas.getContext('2d');
    const video = document.getElementById('sketchy-video');
    const streamTitle = %q;

    let tick = 0;
    function renderStream() {
      tick++;
      const t = tick * 0.03;
      const g = ctx.createRadialGradient(640, 320, 40, 640, 360, 680);
      g.addColorStop(0, '#1e1b4b');
      g.addColorStop(0.6, '#0f172a');
      g.addColorStop(1, '#020617');
      ctx.fillStyle = g;
      ctx.fillRect(0, 0, 1280, 720);

      // Animated equalizer bars & cinema waveform
      for (let i = 0; i < 42; i++) {
        const h = 30 + Math.abs(Math.sin(t * 1.7 + i * 0.35)) * 160;
        ctx.fillStyle = i %% 2 === 0 ? '#38bdf8' : '#a855f7';
        ctx.fillRect(180 + i * 22, 420 - h / 2, 14, h);
      }

      ctx.fillStyle = '#ffffff';
      ctx.font = 'bold 34px system-ui, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(streamTitle.toUpperCase(), 640, 130);

      ctx.fillStyle = '#10b981';
      ctx.font = 'bold 18px monospace';
      ctx.fillText('✓ SANITIZED BY ITEMBA-SHIELD  •  ALL OVERLAYS & TRACKERS REMOVED', 640, 175);

      ctx.fillStyle = '#94a3b8';
      ctx.font = '15px monospace';
      ctx.fillText('Click anywhere on the video or press "Unified Theater Mode" — zero pop-ups will fire.', 640, 660);

      requestAnimationFrame(renderStream);
    }
    renderStream();

    if (canvas.captureStream) {
      video.srcObject = canvas.captureStream(30);
      video.play().catch(() => {});
    }
  </script>
</body>
</html>`
