# Gomodel Async Video Generation

## How to run it
clone repository locally

```bash
docker compose down
docker compose up --build
```

## Requests Examples
```bash
curl.exe http://localhost:8080/v1/videos -H "Authorization: Bearer my-local-master-key-123" -H "Content-Type: application/json" -d '@gomodel_test_request.json'

curl.exe http://localhost:8080/v1/videos/<id> -H "Authorization: Bearer my-local-master-key-123"

curl.exe http://localhost:8080/v1/videos/<id>/content -H "Authorization: Bearer my-local-master-key-123" --output generated_video.mp4
```

## Notes
- For any request, Authorization header with API key "my-local-master-key-123" is required
- When you make a POST request to create a video, there will be a JSON response. Take the "id" field from the response and insert it in the <id> placeholders to make GET requests for that job ID
- Edit gomodel_test_request.json to edit PUT request data
- Edit docker-compose.yml to alter mock video backend env variables
- Outputted mp4 file is created in the same directory