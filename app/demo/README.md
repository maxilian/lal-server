
start ws-flv pull

```
curl -X POST http://localhost:8083/api/ctrl/start_wsflv_pull \
  -H "Content-Type: application/json" \
  -d '{
    "url": "ws://localhost:8091/percobaan/testing-latency.flv",
    "app_name": "live",
    "stream_name": "restreamed"
  }'
```

Stop ws-flv pull

```
curl -X POST http://localhost:8083/api/ctrl/stop_wsflv_pull \
-d '{
 "app_name":"live",
 "stream_name":"restreamed"
}'

```

get streaming stats by stream_name

```
http://localhost:8083/api/stat/group?stream_name=restreamed
```