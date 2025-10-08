-- enable pgvector
CREATE EXTENSION IF NOT EXISTS vector;

-- table to store embeddings per remark/product/year
CREATE TABLE IF NOT EXISTS remark_embeddings (
    id SERIAL PRIMARY KEY,
    product TEXT NOT NULL,
    remark  TEXT NOT NULL,
    year    INT  NOT NULL,
    embedding VECTOR(768),
    metadata JSONB,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (product, remark, year)
);

-- fast ANN index (cosine)
CREATE INDEX IF NOT EXISTS remark_embeddings_idx
ON remark_embeddings
USING ivfflat (embedding vector_cosine_ops)
WITH (lists = 100);

-- recommended: analyze table for planner
ANALYZE remark_embeddings;
