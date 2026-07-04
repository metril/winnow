// Hand-rolled hash routing — deliberately dumb (parse/format only, no router
// dependency). The hash is the single source of truth for which view is open
// plus its params (e.g. #/usage/34615562/week/2026-06-28, #/meters/123), so
// views are deep-linkable and survive refresh; state-based nav previously reset
// to Overview on every reload.
import { useCallback, useEffect, useState } from "react";
import { View } from "./components/shell";

export interface Route {
  view: View;
  params: string[];
}

const VIEWS: View[] = [
  "overview", "usage", "identify", "meters", "loadtests", "utility",
  "devices", "agents", "maintenance", "settings",
];

function parse(): Route {
  const parts = window.location.hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  const v = parts[0] as View;
  if (VIEWS.includes(v)) return { view: v, params: parts.slice(1).map(decodeURIComponent) };
  return { view: "overview", params: [] };
}

function format(view: View, params: (string | number)[]) {
  return "#/" + [view, ...params.map((p) => encodeURIComponent(String(p)))].join("/");
}

export function useHashRoute(): [Route, (view: View, params?: (string | number)[]) => void] {
  const [route, setRoute] = useState<Route>(parse);
  useEffect(() => {
    const on = () => setRoute(parse());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const nav = useCallback((view: View, params: (string | number)[] = []) => {
    const h = format(view, params);
    if (window.location.hash !== h) window.location.hash = h; // hashchange updates state
    else setRoute(parse());
  }, []);
  return [route, nav];
}

// replaceHash mirrors in-view state (like the usage period) into the hash
// without a history entry or re-render — refresh restores it, back/forward
// isn't spammed by every arrow click.
export function replaceHash(view: View, params: (string | number)[] = []) {
  history.replaceState(null, "", format(view, params));
}
