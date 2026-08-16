#!/usr/bin/env bash
# v6pool rotation check - prints every exit IP and how many are distinct
# usage: ./check-rotation.sh [PROXY_URL] [REQUESTS] [URL]
#   http:  ./check-rotation.sh 'http://user:pass@HOST:3128'
#   socks: ./check-rotation.sh 'socks5h://user:pass@HOST:1080'

PROXY="${1:?usage: $0 'http://USER:PASS@HOST:8080' [requests] [url]}"
REQUESTS="${2:-10}"
URL="${3:-https://api6.ipify.org}"

echo "checking rotation via $PROXY ($REQUESTS requests)..."
ips=()
for i in $(seq 1 "$REQUESTS"); do
  ip=$(curl -s --max-time 20 -x "$PROXY" "$URL" | tr -d '\r\n')
  if [[ -n "$ip" ]]; then
    ips+=("$ip")
    printf '  %2d: %s\n' "$i" "$ip"
  else
    printf '  %2d: FAILED\n' "$i"
  fi
done

total=${#ips[@]}
distinct=$(printf '%s\n' "${ips[@]}" | sort -u | wc -l)
echo "---------------------------------------------"
echo "distinct exit IPs: $distinct / $total"
if (( total >= 2 && distinct == 1 )); then
  echo "no rotation - all requests share one IP"
elif (( distinct == total )); then
  echo "full rotation - every request got a new IP"
else
  echo "partial rotation (some IPs reused)"
fi
