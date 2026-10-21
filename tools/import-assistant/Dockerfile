FROM mcr.microsoft.com/playwright/python:v1.63.0-noble
WORKDIR /assistant
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY core.py worker.py browser_login.py server.py assistant.html test_*.py ./
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 DISPLAY=:99
USER pwuser
EXPOSE 8090
CMD ["/bin/sh", "-c", "umask 077; Xvfb :99 -screen 0 1100x850x24 -nolisten tcp >/dev/null 2>&1 & exec python -m uvicorn server:app --host 0.0.0.0 --port 8090 --no-access-log --log-level warning"]
