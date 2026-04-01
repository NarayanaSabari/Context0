# PostgreSQL + Apache AGE + pgvector
# Based on the official AGE image, adding pgvector for hybrid search
FROM apache/age:latest

USER root

# Install build deps for pgvector
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      build-essential \
      git \
      ca-certificates \
      postgresql-server-dev-18 \
    && rm -rf /var/lib/apt/lists/*

# Build and install pgvector from source
RUN git clone https://github.com/pgvector/pgvector.git /tmp/pgvector && \
    cd /tmp/pgvector && \
    make OPTFLAGS="" && \
    make install && \
    rm -rf /tmp/pgvector

# Clean up build deps to keep image small
RUN apt-get purge -y --auto-remove build-essential git postgresql-server-dev-18

USER postgres
