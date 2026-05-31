# --- build the React SPA -----------------------------------------------------
FROM node:20-bookworm-slim AS frontend
WORKDIR /fe
COPY app/frontend/package.json app/frontend/package-lock.json* ./
RUN npm install
COPY app/frontend/ ./
RUN npm run build

# --- python runtime ----------------------------------------------------------
FROM python:3.12-slim
WORKDIR /app

COPY app/requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

# Shared data layer + API; built SPA from the frontend stage.
COPY meterfinder /app/meterfinder
COPY app/meterfinder_api /app/meterfinder_api
COPY --from=frontend /fe/dist /app/static

ENV STATIC_DIR=/app/static \
    PYTHONUNBUFFERED=1
EXPOSE 8000

CMD ["uvicorn", "meterfinder_api.main:app", "--host", "0.0.0.0", "--port", "8000"]
