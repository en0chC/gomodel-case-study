# Stage 1: bake a tiny valid mp4 (2s test pattern, 864x480) so /content returns a real video
FROM alpine:3.20 AS sample
RUN apk add --no-cache ffmpeg
RUN ffmpeg -loglevel error -f lavfi -i testsrc=size=864x480:rate=24 -t 2 \
      -c:v libx264 -pix_fmt yuv420p -movflags +faststart /sample.mp4

# Stage 2: the app
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY app.py .
COPY --from=sample /sample.mp4 ./sample.mp4
ENV MOCK_STORAGE_PATH=/tmp/mock-videos
EXPOSE 8000
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
