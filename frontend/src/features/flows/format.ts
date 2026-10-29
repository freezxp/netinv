// How flow values read. The API returns every group key as text; these turn
// the numeric codes into what an operator recognises — "TCP", "EF", "SYN,ACK",
// "Malaysia" — while keeping the raw value for filtering.
import type { FlowLabels } from "./api";

const PROTOCOLS: Record<string, string> = {
  "1": "ICMP",
  "2": "IGMP",
  "6": "TCP",
  "17": "UDP",
  "41": "IPv6",
  "47": "GRE",
  "50": "ESP",
  "51": "AH",
  "58": "ICMPv6",
  "89": "OSPF",
  "112": "VRRP",
  "132": "SCTP",
};

// Well-known service ports, for the port column's second line only.
const SERVICES: Record<string, string> = {
  "20": "ftp-data",
  "21": "ftp",
  "22": "ssh",
  "23": "telnet",
  "25": "smtp",
  "53": "dns",
  "67": "dhcp",
  "80": "http",
  "110": "pop3",
  "123": "ntp",
  "143": "imap",
  "161": "snmp",
  "389": "ldap",
  "443": "https",
  "445": "smb",
  "500": "ike",
  "514": "syslog",
  "587": "submission",
  "636": "ldaps",
  "993": "imaps",
  "1194": "openvpn",
  "1433": "mssql",
  "1812": "radius",
  "3306": "mysql",
  "3389": "rdp",
  "4500": "ipsec-nat",
  "5060": "sip",
  "5432": "postgres",
  "8080": "http-alt",
  "8443": "https-alt",
  "51820": "wireguard",
};

const DSCP: Record<string, string> = {
  "0": "BE",
  "8": "CS1",
  "10": "AF11",
  "12": "AF12",
  "14": "AF13",
  "16": "CS2",
  "18": "AF21",
  "20": "AF22",
  "22": "AF23",
  "24": "CS3",
  "26": "AF31",
  "28": "AF32",
  "30": "AF33",
  "32": "CS4",
  "34": "AF41",
  "36": "AF42",
  "38": "AF43",
  "40": "CS5",
  "44": "VA",
  "46": "EF",
  "48": "CS6",
  "56": "CS7",
};

const FW_EVENTS: Record<string, string> = {
  "0": "—",
  "1": "created",
  "2": "deleted",
  "3": "denied",
  "4": "alert",
  "5": "update",
};

let regions: Intl.DisplayNames | null = null;
function countryName(code: string): string {
  try {
    regions ??= new Intl.DisplayNames(["en"], { type: "region" });
    return regions.of(code) ?? code;
  } catch {
    return code;
  }
}

export function tcpFlags(v: number): string {
  if (!v) return "—";
  const names = ["FIN", "SYN", "RST", "PSH", "ACK", "URG", "ECE", "CWR"];
  return names.filter((_, i) => v & (1 << i)).join(",");
}

/** Context a value needs: the exporter of its row, for interface names. */
export interface ValueContext {
  labels?: FlowLabels;
  exporter?: string;
}

/**
 * Display text for a value of a field kind, and an optional dimmer second
 * part (the service name beside a port, the code beside a country).
 */
export function renderValue(
  kind: string,
  raw: string | number,
  ctx: ValueContext = {},
): { text: string; hint?: string } {
  const v = String(raw);
  switch (kind) {
    case "proto":
      return { text: PROTOCOLS[v] ?? v, hint: PROTOCOLS[v] ? v : undefined };
    case "port":
      return { text: v === "0" ? "—" : v, hint: SERVICES[v] };
    case "dscp":
      return { text: DSCP[v] ?? v, hint: DSCP[v] ? v : undefined };
    case "flags":
      return { text: tcpFlags(Number(v)) };
    case "direction":
      return { text: v === "1" ? "ingress" : v === "2" ? "egress" : "—" };
    case "country":
      return v ? { text: countryName(v), hint: v } : { text: "—" };
    case "as": {
      if (v === "0") return { text: "—" };
      const [num, ...name] = v.split(" ");
      return { text: name.join(" ") || `AS${num}`, hint: name.length ? `AS${num}` : undefined };
    }
    case "exporter": {
      const dev = ctx.labels?.exporters[v];
      return dev ? { text: dev.device_name, hint: v } : { text: v };
    }
    case "iface": {
      // Group keys arrive as "exporter|ifIndex"; a raw flow row has the bare
      // ifIndex and its exporter beside it.
      const key = v.includes("|") ? v : `${ctx.exporter ?? ""}|${v}`;
      const idx = key.split("|")[1];
      if (idx === "0") return { text: "—" };
      const name = ctx.labels?.interfaces[key];
      return name ? { text: name, hint: `#${idx}` } : { text: `#${idx}` };
    }
    case "addr":
      return { text: v === "::" ? "—" : v };
    case "number":
      return { text: v === "0" ? "—" : v };
    case "text":
    case "mac":
      return { text: v === "" || v === "00:00:00:00:00:00" ? "—" : v };
  }
  return { text: v };
}

export function fwEvent(v: string | number): string {
  return FW_EVENTS[String(v)] ?? String(v);
}

/** The filter value a rendered value stands for (an AS key carries a name). */
export function filterValue(kind: string, raw: string): string {
  return kind === "as" ? raw.split(" ")[0] : raw;
}

export function formatCount(n: number): string {
  if (n < 10) return n === Math.round(n) ? String(n) : n.toFixed(1);
  if (n < 1000) return String(Math.round(n));
  const units = ["", "k", "M", "G", "T"];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)}${units[i]}`;
}
