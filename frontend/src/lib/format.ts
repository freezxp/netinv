// Unit formatters (doc 14): bps, durations, percentages.

export function formatBps(bps: number): string {
  if (!isFinite(bps) || bps === 0) return "0 bps";
  const units = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  let i = 0;
  let v = bps;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

// Volume rather than rate: flow tables rank on bytes moved over the visible
// range, which is a different quantity from the bits/sec on the chart beside
// it and must not borrow that formatter.
export function formatBytes(bytes: number): string {
  if (!isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  let v = bytes;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export function formatMs(seconds: number): string {
  const ms = seconds * 1000;
  if (ms < 1) return `${(ms * 1000).toFixed(0)} µs`;
  if (ms < 100) return `${ms.toFixed(1)} ms`;
  return `${ms.toFixed(0)} ms`;
}

export function formatDuration(totalSeconds: number): string {
  if (totalSeconds < 60) return `${Math.round(totalSeconds)}s`;
  if (totalSeconds < 3600) return `${Math.floor(totalSeconds / 60)}m`;
  if (totalSeconds < 86400)
    return `${Math.floor(totalSeconds / 3600)}h ${Math.floor((totalSeconds % 3600) / 60)}m`;
  return `${Math.floor(totalSeconds / 86400)}d ${Math.floor((totalSeconds % 86400) / 3600)}h`;
}

export function formatPercent(v: number | undefined): string {
  return v === undefined ? "—" : `${v.toFixed(v >= 100 ? 0 : 1)}%`;
}

/**
 * The interface an alert is about, as a human would name it.
 *
 * Alert labels carry `if_index` always, and `if_name`/`if_alias` when inventory
 * knew them at fire time (doc 03 FR-ALR-08). An ifIndex is the least useful of
 * the three to whoever is reading the alert: it identifies the port only to the
 * device, changes across reboots, and says nothing about what the port is for.
 * ifAlias is usually the most useful, because it is the operator's own
 * description — an uplink, a customer, a circuit id.
 *
 * Returns an empty string for alerts that are not interface-scoped, so callers
 * can drop the segment entirely rather than print a stray separator.
 */
export function formatAlertInterface(labels: Record<string, string>): string {
  const name = labels.if_name?.trim();
  const alias = labels.if_alias?.trim();
  const idx = labels.if_index?.trim();
  if (name && alias && alias !== name) return `${name} — ${alias}`;
  if (name) return name;
  // An alias with no name still beats the index; some agents expose only one.
  if (alias) return alias;
  return idx ? `if ${idx}` : "";
}
