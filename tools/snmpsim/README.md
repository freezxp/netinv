# SNMP simulator data

`snmpsim` serves each `data/*.snmprec` file as a simulated device; the filename
is the SNMPv2c community string (`public.snmprec` → community `public`), reachable
on UDP 1161 in the compose stack.

Test from the host:

```bash
snmpget -v2c -c public 127.0.0.1:1161 1.3.6.1.2.1.1.5.0
```

Record new fixtures from real devices with `scripts/walk-recorder` (Sprint 6+)
or `snmprec.py` from the snmpsim package. Per doc 24 §2 the target set grows to
12 devices covering all 5 vendors plus edge cases (32-bit-only counters, v3
authPriv, timeout-prone agents, ifIndex shifts).
