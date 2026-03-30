-- +goose Up
-- Maxfiylik siyosatiga haydovchilik guvohnomasi va texnik pasport (haydovchilar uchun) qo‘shiladi; faol versiya 3.
UPDATE legal_documents SET is_active = 0 WHERE document_type = 'privacy_policy' AND is_active = 1;

INSERT INTO legal_documents (document_type, version, content, is_active) VALUES
('privacy_policy', 3,
'📄 Maxfiylik siyosati

YettiQanot foydalanuvchi ma’lumotlarini xizmatni ta’minlash uchun qayta ishlaydi.

1. Yig‘iladigan ma’lumotlar:
- telefon raqam
- Telegram ID
- joylashuv (location)
- buyurtma ma’lumotlari
- haydovchilar uchun: haydovchilik guvohnomasi bo‘yicha ma’lumotlar (jumladan, surat yoki skan-nusxa orqali taqdim etilishi mumkin)
- haydovchilar uchun: transport vositasi texnik pasporti bo‘yicha ma’lumotlar (jumladan, surat yoki skan-nusxa orqali taqdim etilishi mumkin)

2. Maqsad:
- haydovchi va mijozni bog‘lash
- buyurtmalarni uzatish
- xavfsizlik
- haydovchini va transport vositasini identifikatsiya qilish hamda tekshirish

3. Ma’lumotlar sotilmaydi.

4. Platformadan foydalanish orqali siz rozilik bildirasiz.',
1);

-- +goose Down
DELETE FROM legal_documents WHERE document_type = 'privacy_policy' AND version = 3;
UPDATE legal_documents SET is_active = 1 WHERE document_type = 'privacy_policy' AND version = 2;
