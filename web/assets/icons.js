(function () {
  function svg(pathD, extra) {
    return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" ${extra || ''}>${pathD}</svg>`;
  }

  const icons = {
    bolt: (cls) => svg('<path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"/>', cls),
    settings: (cls) => svg('<path d="M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7z"/><path d="M19.4 15a7.9 7.9 0 0 0 .1-2l2-1.2-2-3.5-2.2.7a7.4 7.4 0 0 0-1.7-1l-.2-2.3H10.6l-.2 2.3c-.6.2-1.2.6-1.7 1l-2.2-.7-2 3.5 2 1.2a7.9 7.9 0 0 0 .1 2l-2 1.2 2 3.5 2.2-.7c.5.4 1.1.8 1.7 1l.2 2.3h3.8l.2-2.3c.6-.2 1.2-.6 1.7-1l2.2.7 2-3.5-2-1.2z"/>', cls),
    dashboard: (cls) => svg('<path d="M3 13h8V3H3v10z"/><path d="M13 21h8V11h-8v10z"/><path d="M13 3h8v6h-8V3z"/><path d="M3 21h8v-6H3v6z"/>', cls),
    dots: (cls) => svg('<path d="M12 5h.01"/><path d="M12 12h.01"/><path d="M12 19h.01"/>', cls),
    chevronDown: (cls) => svg('<path d="M6 9l6 6 6-6"/>', cls),
    chevronUp: (cls) => svg('<path d="M18 15l-6-6-6 6"/>', cls),
    close: (cls) => svg('<path d="M18 6L6 18"/><path d="M6 6l12 12"/>', cls),
    refresh: (cls) => svg('<path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/>', cls),
    wifi: (cls) => svg('<path d="M5 12.5a10 10 0 0 1 14 0"/><path d="M8.5 16a5 5 0 0 1 7 0"/><path d="M12 20h0"/>', cls),
    wifiOff: (cls) => svg('<path d="M2 2l20 20"/><path d="M5 12.5a10 10 0 0 1 4-2.8"/><path d="M8.5 16a5 5 0 0 1 3.5-1.4"/><path d="M16 16a5 5 0 0 1 1 1"/><path d="M19 12.5a10 10 0 0 0-8-3"/><path d="M12 20h0"/>', cls),
    power: (cls) => svg('<path d="M12 2v10"/><path d="M7.8 4.2a8 8 0 1 0 8.4 0"/>', cls),
    plug: (cls) => svg('<path d="M9 7v5"/><path d="M15 7v5"/><path d="M8 11h8"/><path d="M12 11v10"/><path d="M10 21h4"/>', cls),
    washer: (cls) => svg('<path d="M6 2h12v20H6V2z"/><path d="M6 6h12"/><path d="M9 4h0"/><path d="M12 4h0"/><path d="M15 4h0"/><path d="M12 17a4 4 0 1 0 0-8 4 4 0 0 0 0 8z"/>', cls),
    dryer: (cls) => svg('<path d="M6 2h12v20H6V2z"/><path d="M6 6h12"/><path d="M9 4h0"/><path d="M12 4h0"/><path d="M15 4h0"/><path d="M9 14c1 2 3 3 6 3"/><path d="M15 11c-1-2-3-3-6-3"/>', cls),
    tv: (cls) => svg('<rect x="3" y="7" width="18" height="12" rx="2"/><path d="M7 21h10"/><path d="M12 19v2"/>', cls),
    snowflake: (cls) => svg('<path d="M12 2v20"/><path d="M4.9 4.9l14.2 14.2"/><path d="M2 12h20"/><path d="M4.9 19.1L19.1 4.9"/>', cls),
    fan: (cls) => svg('<path d="M12 12a2 2 0 1 0 0 0"/><path d="M12 10c0-5 4-6 6-4-2 4-5 4-6 4z"/><path d="M14 12c5 0 6 4 4 6-4-2-4-5-4-6z"/><path d="M12 14c0 5-4 6-6 4 2-4 5-4 6-4z"/><path d="M10 12c-5 0-6-4-4-6 4 2 4 5 4 6z"/>', cls),
    fridge: (cls) => svg('<path d="M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z"/><path d="M5 10h14"/><path d="M9 6h0"/><path d="M9 14h0"/>', cls),
    oven: (cls) => svg('<path d="M6 4h12v16H6V4z"/><path d="M6 8h12"/><path d="M9 6h0"/><path d="M12 6h0"/><path d="M15 6h0"/><path d="M9 12h6"/><path d="M9 15h6"/>', cls),
    microwave: (cls) => svg('<path d="M4 7h16v10H4V7z"/><path d="M7 10h7"/><path d="M7 13h6"/><path d="M17 10h0"/><path d="M17 13h0"/>', cls),
    dishwasher: (cls) => svg('<path d="M6 2h12v20H6V2z"/><path d="M6 6h12"/><path d="M9 4h0"/><path d="M12 4h0"/><path d="M15 4h0"/><path d="M9 14c.8 1.6 2.4 3 3 3s2.2-1.4 3-3"/>', cls),
    vacuum: (cls) => svg('<path d="M9 3h6"/><path d="M8 6h8"/><path d="M7 9h10"/><path d="M6 12a6 6 0 0 0 12 0"/><path d="M12 18v3"/><path d="M10 21h4"/>', cls),
    speaker: (cls) => svg('<path d="M8 8h3l5-4v16l-5-4H8V8z"/><path d="M19 9a4 4 0 0 1 0 6"/><path d="M21 7a7 7 0 0 1 0 10"/>', cls),
    light: (cls) => svg('<path d="M9 18h6"/><path d="M10 22h4"/><path d="M12 2a7 7 0 0 0-4 12c.8.7 1.2 2 1.2 3H14.8c0-1 .4-2.3 1.2-3a7 7 0 0 0-4-12z"/>', cls),
    clock: (cls) => svg('<path d="M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20z"/><path d="M12 6v6l4 2"/>', cls),
    lock: (cls) => svg('<rect x="5" y="11" width="14" height="11" rx="2"/><path d="M7 11V8a5 5 0 0 1 10 0v3"/>', cls),
    unlock: (cls) => svg('<rect x="5" y="11" width="14" height="11" rx="2"/><path d="M9 11V8a3 3 0 0 1 6 0"/>', cls),
    volume: (cls) => svg('<path d="M11 5L6 9H3v6h3l5 4V5z"/><path d="M15.5 8.5a4 4 0 0 1 0 7"/><path d="M18 6a7 7 0 0 1 0 12"/>', cls),
    mute: (cls) => svg('<path d="M11 5L6 9H3v6h3l5 4V5z"/><path d="M16 9l5 6"/><path d="M21 9l-5 6"/>', cls),
    play: (cls) => svg('<path d="M8 5v14l11-7z"/>', cls),
    pause: (cls) => svg('<path d="M6 5h4v14H6z"/><path d="M14 5h4v14h-4z"/>', cls),
    stop: (cls) => svg('<path d="M6 6h12v12H6z"/>', cls),
    list: (cls) => svg('<path d="M8 6h13"/><path d="M8 12h13"/><path d="M8 18h13"/><path d="M3 6h.01"/><path d="M3 12h.01"/><path d="M3 18h.01"/>', cls),
  };

  function icon(name, className) {
    const fn = icons[name];
    const cls = className ? `class="${className}"` : '';
    return fn ? fn(cls) : '';
  }

  window.hnIcons = { icon };
})();
