ALTER TABLE orders
    ADD UNIQUE INDEX uq_orders_request_id (request_id);
