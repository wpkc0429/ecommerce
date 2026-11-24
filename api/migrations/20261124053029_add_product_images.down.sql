-- reverse: create index "productimage_product_id_position_id" to table: "product_images"
DROP INDEX "productimage_product_id_position_id";
-- reverse: create index "product_images_image_key_key" to table: "product_images"
DROP INDEX "product_images_image_key_key";
-- reverse: create "product_images" table
DROP TABLE "product_images";
-- reverse: create index "mediaupload_shop_id_id" to table: "media_uploads"
DROP INDEX "mediaupload_shop_id_id";
-- reverse: create "media_uploads" table
DROP TABLE "media_uploads";
