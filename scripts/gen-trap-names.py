#!/usr/bin/env python3
"""Regenerate backend/internal/events/names_vendor.go from vendor MIB text (doc 36 §3).

    mkdir mibs && cd mibs
    B=https://raw.githubusercontent.com/librenms/librenms/master/mibs
    curl -fLO $B/fortinet/FORTINET-CORE-MIB -fLO $B/fortinet/FORTINET-FORTIGATE-MIB \
         -fLO $B/paloaltonetworks/PAN-TRAPS
    cd .. && python3 -I scripts/gen-trap-names.py mibs > backend/internal/events/names_vendor.go
    gofmt -w backend/internal/events/names_vendor.go

Only notifications under the trap roots below are taken; anything else is
reported on stderr and skipped rather than guessed at.
"""
import re,sys
d=sys.argv[1]
roots={'fgTrapPrefix':'1.3.6.1.4.1.12356.101.2.0','fgFmTrapPrefix':'1.3.6.1.4.1.12356.101.6.0',
       'fnTrapsPrefix':'1.3.6.1.4.1.12356.100.1.3.0','panCommonEventEventsV2':'1.3.6.1.4.1.25461.2.1.3.2.0'}
out=[]
for f in ('FORTINET-CORE-MIB','FORTINET-FORTIGATE-MIB','PAN-TRAPS'):
    t=open(f'{d}/{f}').read()
    for m in re.finditer(r'^\s*(\w+)\s+NOTIFICATION-TYPE\b.*?::=\s*\{\s*(\w+)\s+(\d+)\s*\}',t,re.S|re.M):
        name,par,n=m.groups()
        if par not in roots: print('skip',f,name,par,file=sys.stderr); continue
        out.append((f,roots[par]+'.'+n,name))
seen={}
for f,o,n in out:
    assert o not in seen or seen[o]==n,(o,n,seen.get(o)); seen[o]=n
print(len(seen),file=sys.stderr)
w=sys.stdout.write
w('// Code generated from the published vendor MIBs; DO NOT EDIT by hand.\n')
w('// Sources: FORTINET-CORE-MIB, FORTINET-FORTIGATE-MIB (Fortinet) and PAN-TRAPS\n')
w('// (Palo Alto Networks), as mirrored in the LibreNMS MIB collection. See doc 36 §3.\n\n')
w('package events\n\n// vendorTrapNames: names taken from the MIB text, unverified against hardware.\nvar vendorTrapNames = map[string]string{\n')
cur=None
for f,o,n in out:
    if f!=cur: w(f'\t// {f}\n'); cur=f
    w(f'\t"{o}": "{n}",\n')
w('}\n')
