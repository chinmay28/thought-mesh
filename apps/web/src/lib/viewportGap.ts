/**
 * How far the painted canvas extends below where `position: fixed` lands.
 *
 * With `viewport-fit=cover` the page paints to the physical bottom of the
 * screen, but iOS anchors fixed elements to the *layout* viewport, which can
 * be shorter than that. On an installed iPhone web app the whole fixed layer
 * — tab bar and floating button alike — came to rest 62px above the bottom of
 * the screen, with page background showing through underneath.
 *
 * `visualViewport` measures what is actually visible; `clientHeight` measures
 * the layout viewport the fixed layer is pinned to. The difference is the
 * strip nothing can reach, published as `--viewport-gap` on the root element.
 * It is 0 wherever the two agree — every desktop browser, Android, and iOS
 * when it behaves — which makes the rules that read it no-ops.
 */
export function trackViewportGap(): () => void {
  const root = document.documentElement;
  const vv = window.visualViewport;

  const update = () => {
    // Pinch-zoom makes the visible area a window onto the page rather than a
    // measure of it, so the comparison means nothing while zoomed in.
    const gap = vv && vv.scale <= 1.01 ? vv.height + vv.offsetTop - root.clientHeight : 0;
    // An open keyboard shrinks the visual viewport (negative), and nothing
    // legitimate is more than a toolbar tall — clamp rather than trust it.
    root.style.setProperty('--viewport-gap', `${Math.min(Math.max(Math.round(gap), 0), 200)}px`);
  };

  update();
  vv?.addEventListener('resize', update);
  window.addEventListener('orientationchange', update);
  return () => {
    vv?.removeEventListener('resize', update);
    window.removeEventListener('orientationchange', update);
  };
}
