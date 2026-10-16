-- Default taxonomy: Plaid PFC v2 primaries adapted for CZ/SK. Codes are referenced by
-- internal/domain/importing/bankhints.go and coin-book-web theme tokens (color_key).
INSERT INTO budget_group (code, name_cs, name_sk, sort) VALUES
    ('fixed',    'Fixní výdaje',     'Fixné výdavky',     10),
    ('flexible', 'Flexibilní výdaje', 'Flexibilné výdavky', 20),
    ('goals',    'Cíle a spoření',   'Ciele a sporenie',  30);

INSERT INTO category (code, name_cs, name_sk, kind, color_key, icon, budget_group_id, sort)
SELECT c.code, c.name_cs, c.name_sk, c.kind, c.color_key, c.icon, g.id, c.sort
FROM (VALUES
    ('housing',    'Bydlení a energie',       'Bývanie a energie',         'expense',   'chrpa',      'house',          'fixed',    10),
    ('groceries',  'Potraviny a drogerie',    'Potraviny a drogéria',      'expense',   'smaragd',    'shopping-cart',  'flexible', 20),
    ('dining',     'Jídlo a pití venku',      'Jedlo a pitie vonku',       'expense',   'malina',     'fork-knife',     'flexible', 30),
    ('transport',  'Doprava',                 'Doprava',                   'expense',   'mandarinka', 'car',            'flexible', 40),
    ('health',     'Zdraví a péče',           'Zdravie a starostlivosť',   'expense',   'laguna',     'heartbeat',      'flexible', 50),
    ('children',   'Děti a vzdělání',         'Deti a vzdelanie',          'expense',   'limeta',     'baby',           'flexible', 60),
    ('shopping',   'Nákupy',                  'Nákupy',                    'expense',   'slunecnice', 'shopping-bag',   'flexible', 70),
    ('leisure',    'Zábava a volný čas',      'Zábava a voľný čas',        'expense',   'ametyst',    'ticket',         'flexible', 80),
    ('services',   'Služby, pojištění a daně', 'Služby, poistenie a dane', 'expense',   'kakao',      'receipt',        'fixed',    90),
    ('other',      'Ostatní výdaje',          'Ostatné výdavky',           'expense',   'bridlice',   'dots-three',     'flexible', 100),
    ('financial',  'Spoření a splátky',       'Sporenie a splátky',        'financial', 'bridlice',   'piggy-bank',     'goals',    110),
    ('income',     'Příjmy',                  'Príjmy',                    'income',    'income',     'arrow-down-left', NULL,      120),
    ('transfer',   'Převody',                 'Prevody',                   'transfer',  'transfer',   'arrows-left-right', NULL,    130)
) AS c (code, name_cs, name_sk, kind, color_key, icon, group_code, sort)
LEFT JOIN budget_group g ON g.code = c.group_code;

INSERT INTO category (code, parent_id, name_cs, name_sk, kind, sort)
SELECT s.code, p.id, s.name_cs, s.name_sk, p.kind, s.sort
FROM (VALUES
    ('housing.rent',             'housing',   'Nájem',                          'Nájom',                          10),
    ('housing.hoa_fund',         'housing',   'Fond oprav SVJ',                 'Fond opráv',                     20),
    ('housing.energy',           'housing',   'Elektřina a plyn',               'Elektrina a plyn',               30),
    ('housing.water',            'housing',   'Vodné a stočné',                 'Vodné a stočné',                 40),
    ('housing.sipo',             'housing',   'SIPO',                           'SIPO',                           50),
    ('housing.insurance',        'housing',   'Pojištění domácnosti',           'Poistenie domácnosti',           60),
    ('housing.maintenance',      'housing',   'Dům, zahrada a údržba',          'Dom, záhrada a údržba',          70),
    ('groceries.food',           'groceries', 'Potraviny',                      'Potraviny',                      10),
    ('groceries.drugstore',      'groceries', 'Drogerie',                       'Drogéria',                       20),
    ('dining.restaurants',       'dining',    'Restaurace',                     'Reštaurácie',                    10),
    ('dining.cafes',             'dining',    'Kavárny a bary',                 'Kaviarne a bary',                20),
    ('dining.delivery',          'dining',    'Rozvoz jídla',                   'Rozvoz jedla',                   30),
    ('transport.fuel',           'transport', 'Pohonné hmoty',                  'Pohonné hmoty',                  10),
    ('transport.public',         'transport', 'Veřejná doprava a taxi',         'Verejná doprava a taxi',         20),
    ('transport.parking',        'transport', 'Parkování a poplatky',           'Parkovanie a poplatky',          30),
    ('transport.service',        'transport', 'Servis a náhradní díly',         'Servis a náhradné diely',        40),
    ('transport.vignette',       'transport', 'Dálniční známka',                'Diaľničná známka',               50),
    ('health.pharmacy',          'health',    'Lékárna',                        'Lekáreň',                        10),
    ('health.doctor',            'health',    'Lékař',                          'Lekár',                          20),
    ('health.beauty',            'health',    'Krása a péče',                   'Krása a starostlivosť',          30),
    ('children.childcare',       'children',  'Školka a hlídání',               'Škôlka a stráženie',             10),
    ('children.activities',      'children',  'Kroužky a aktivity',             'Krúžky a aktivity',              20),
    ('children.school',          'children',  'Škola a pomůcky',                'Škola a pomôcky',                30),
    ('children.toys',            'children',  'Hračky a potřeby',               'Hračky a potreby',               40),
    ('children.education',       'children',  'Vzdělávání',                     'Vzdelávanie',                    50),
    ('shopping.clothing',        'shopping',  'Oblečení a obuv',                'Oblečenie a obuv',               10),
    ('shopping.electronics',     'shopping',  'Elektronika',                    'Elektronika',                    20),
    ('shopping.home',            'shopping',  'Domácnost a vybavení',           'Domácnosť a vybavenie',          30),
    ('shopping.gifts',           'shopping',  'Dárky a dobročinnost',           'Darčeky a dobročinnosť',         40),
    ('leisure.events',           'leisure',   'Kultura a akce',                 'Kultúra a podujatia',            10),
    ('leisure.sport',            'leisure',   'Sport a koníčky',                'Šport a koníčky',                20),
    ('leisure.travel',           'leisure',   'Cestování',                      'Cestovanie',                     30),
    ('leisure.media',            'leisure',   'Hudba, filmy a knihy',           'Hudba, filmy a knihy',           40),
    ('leisure.subscriptions',    'leisure',   'Předplatné',                     'Predplatné',                     50),
    ('services.telecom',         'services',  'Telefon, internet a TV',         'Telefón, internet a TV',         10),
    ('services.insurance',       'services',  'Osobní pojištění',               'Osobné poistenie',               20),
    ('services.social_health',   'services',  'Zdravotní a sociální pojištění', 'Zdravotné a sociálne poistenie', 30),
    ('services.taxes',           'services',  'Daně',                           'Dane',                           40),
    ('services.bank_fees',       'services',  'Bankovní poplatky a úroky',      'Bankové poplatky a úroky',       50),
    ('other.cash',               'other',     'Výběr hotovosti',                'Výber hotovosti',                10),
    ('other.pets',               'other',     'Domácí zvířata',                 'Domáce zvieratá',                20),
    ('other.vices',              'other',     'Alkohol a tabák',                'Alkohol a tabak',                30),
    ('other.misc',               'other',     'Nezařazené',                     'Nezaradené',                     40),
    ('financial.savings',        'financial', 'Pravidelné spoření',             'Pravidelné sporenie',            10),
    ('financial.building',       'financial', 'Stavební spoření',               'Stavebné sporenie',              20),
    ('financial.pension',        'financial', 'Penzijní spoření',               'Dôchodkové sporenie',            30),
    ('financial.investments',    'financial', 'Investice',                      'Investície',                     40),
    ('financial.loan_repayment', 'financial', 'Splátky úvěrů',                  'Splátky úverov',                 50),
    ('income.salary',            'income',    'Výplata',                        'Výplata',                        10),
    ('income.interest',          'income',    'Úroky a výnosy',                 'Úroky a výnosy',                 20),
    ('income.refunds',           'income',    'Vratky',                         'Vratky',                         30),
    ('income.loan',              'income',    'Připsání úvěru',                 'Pripísanie úveru',               40),
    ('income.other',             'income',    'Ostatní příjmy',                 'Ostatné príjmy',                 50),
    ('transfer.own',             'transfer',  'Mezi vlastními účty',            'Medzi vlastnými účtami',         10),
    ('transfer.exchange',        'transfer',  'Výměna měn',                     'Výmena mien',                    20)
) AS s (code, parent_code, name_cs, name_sk, sort)
JOIN category p ON p.code = s.parent_code;
