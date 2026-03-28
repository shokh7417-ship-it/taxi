-- +goose Up
-- Rebuild legal_* tables when an incompatible schema exists (e.g. legal_documents without
-- document_type — causes "no such column: document_type" and failed accept flows).
-- Drops acceptance/resume data; users and drivers must accept active documents again.

DROP TABLE IF EXISTS legal_pending_resume;
DROP TABLE IF EXISTS legal_acceptances;
DROP INDEX IF EXISTS idx_legal_documents_one_active_per_type;
DROP TABLE IF EXISTS legal_documents;

CREATE TABLE legal_documents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  document_type TEXT NOT NULL CHECK (document_type IN ('driver_terms','user_terms','privacy_policy')),
  version INTEGER NOT NULL,
  content TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 0 CHECK (is_active IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE (document_type, version)
);

CREATE UNIQUE INDEX idx_legal_documents_one_active_per_type ON legal_documents(document_type) WHERE is_active = 1;

CREATE TABLE legal_acceptances (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  document_type TEXT NOT NULL CHECK (document_type IN ('driver_terms','user_terms','privacy_policy')),
  version INTEGER NOT NULL,
  accepted_at TEXT NOT NULL DEFAULT (datetime('now')),
  client_ip TEXT,
  user_agent TEXT,
  PRIMARY KEY (user_id, document_type)
);

CREATE TABLE legal_pending_resume (
  user_id INTEGER NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  payload TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO legal_documents (document_type, version, content, is_active) VALUES
('driver_terms', 1,
'📄 Haydovchi shartnomasi (oferta)

YettiQanot — haydovchi va mijozni bog‘lovchi platforma bo‘lib, transport xizmatini bevosita ko‘rsatmaydi.

1. Haydovchi mustaqil faoliyat yuritadi va YettiQanot xodimi hisoblanmaydi.

2. Haydovchi quyidagilar uchun to‘liq javobgar:
- transport vositasi holati
- haydovchilik guvohnomasi
- yo‘l harakati qoidalariga rioya qilish

3. YettiQanot faqat buyurtmalarni uzatadi va safar uchun javobgar emas.

4. To‘lovlar haydovchi va mijoz o‘rtasida amalga oshiriladi.

5. Platforma qoidalariga zid harakatlar aniqlansa, hisob bloklanishi mumkin.

6. Referral va bonuslar faqat belgilangan shartlar asosida beriladi.

7. Ushbu shartnomani qabul qilish orqali siz barcha qoidalarga rozilik bildirasiz.',
0),
('user_terms', 1,
'📄 Foydalanuvchi kelishuvi (Rider Terms)

YettiQanot — haydovchi va mijozni bog‘lovchi platforma bo‘lib, transport xizmatini bevosita ko‘rsatmaydi.

1. Platforma faqat buyurtmani haydovchiga uzatadi.

2. Safar haydovchi tomonidan amalga oshiriladi va barcha javobgarlik haydovchiga tegishli.

3. Foydalanuvchi to‘g‘ri manzil kiritishi va haydovchi bilan kelishilgan to‘lovni amalga oshirishi shart.

4. To‘lovlar haydovchi va mijoz o‘rtasida to‘g‘ridan-to‘g‘ri amalga oshiriladi.

5. Platforma transport hodisalari yoki nizolar uchun to‘liq javobgar emas.

6. Platformadagi bonuslar faqat ichki balans hisoblanadi va naqd pulga yechib bo‘lmaydi.

7. Qoidalarga zid harakatlar aniqlansa, akkaunt bloklanishi mumkin.

8. Ushbu kelishuvni qabul qilish orqali siz barcha shartlarga rozilik bildirasiz.',
0),
('privacy_policy', 1,
'📄 Maxfiylik siyosati (v1, arxiv)

YettiQanot foydalanuvchi ma’lumotlarini xizmatni ta’minlash uchun qayta ishlaydi.

1. Yig‘iladigan ma’lumotlar: telefon raqam, Telegram ID, joylashuv, buyurtma ma’lumotlari.

2. Maqsad: haydovchi va mijozni bog‘lash, buyurtmalarni uzatish.

3. Batafsil foydalanish qoidalari /terms orqali mavjud edi.',
0);

INSERT INTO legal_documents (document_type, version, content, is_active) VALUES
('driver_terms', 2,
'📄 Haydovchi shartnomasi (oferta)

YettiQanot — haydovchi va mijozni bog‘lovchi platforma bo‘lib, transport xizmatini bevosita ko‘rsatmaydi.

1. Haydovchi mustaqil faoliyat yuritadi va YettiQanot xodimi hisoblanmaydi.

2. Haydovchi quyidagilar uchun to‘liq javobgar:
- transport vositasi holati
- haydovchilik guvohnomasi
- yo‘l harakati qoidalariga rioya qilish

3. YettiQanot faqat buyurtmalarni uzatadi va safar jarayoni uchun javobgar emas.

4. To‘lovlar mijoz va haydovchi o‘rtasida amalga oshiriladi. YettiQanot hozirda to‘lovlarni qabul qilmaydi.

5. Platforma 5% komissiya qo‘llashi mumkin.
Komissiya platforma qoidalariga muvofiq ichki hisob-kitoblar orqali aks ettiriladi.

6. Platforma haydovchilarga promo kredit (bonus balans) berishi mumkin:
- bu real pul emas
- naqdlashtirilmaydi
- bank hisobiga chiqarilmaydi
- faqat platforma ichida ishlatiladi

7. Platforma qoidalariga zid harakatlar aniqlansa, hisob bloklanishi mumkin.

8. Platforma qoidalari kelgusida yangilanishi mumkin.

9. Ushbu shartnomani qabul qilish orqali siz barcha qoidalarga rozilik bildirasiz.',
1),
('user_terms', 2,
'📄 Foydalanuvchi shartlari

YettiQanot — foydalanuvchilarni haydovchilar bilan bog‘lovchi platformadir.

1. Platforma transport xizmatini bevosita ko‘rsatmaydi.

2. Foydalanuvchi to‘g‘ri ma’lumot kiritishi shart.

3. Joylashuv (location) buyurtma uchun ishlatiladi.

4. To‘lovlar haydovchi bilan to‘g‘ridan-to‘g‘ri amalga oshiriladi.

5. Noto‘g‘ri foydalanish hisob bloklanishiga olib kelishi mumkin.

6. Platforma qoidalari yangilanishi mumkin.

7. Davom etish orqali siz ushbu shartlarga rozilik bildirasiz.',
1),
('privacy_policy', 2,
'📄 Maxfiylik siyosati

YettiQanot foydalanuvchi ma’lumotlarini xizmatni ta’minlash uchun qayta ishlaydi.

1. Yig‘iladigan ma’lumotlar:
- telefon raqam
- Telegram ID
- joylashuv (location)
- buyurtma ma’lumotlari

2. Maqsad:
- haydovchi va mijozni bog‘lash
- buyurtmalarni uzatish
- xavfsizlik

3. Ma’lumotlar sotilmaydi.

4. Platformadan foydalanish orqali siz rozilik bildirasiz.',
1);

UPDATE users SET terms_accepted = 0;
UPDATE drivers SET terms_accepted = 0;

-- +goose Down
DROP TABLE IF EXISTS legal_pending_resume;
DROP TABLE IF EXISTS legal_acceptances;
DROP TABLE IF EXISTS legal_documents;
