"""
Server Money - 設定管理

このファイルは、アプリケーションの設定とログシステムの設定を管理します。
"""

import os
import logging
from logging.handlers import RotatingFileHandler
from datetime import timedelta

class Config:
    """アプリケーション設定クラス"""
    
    # セッション設定
    SECRET_KEY = os.getenv('SECRET_KEY', 'dev-secret-key-change-in-production')
    PERMANENT_SESSION_LIFETIME = timedelta(hours=2)  # 2時間でセッションタイムアウト
    
    # データベース設定
    SQLALCHEMY_DATABASE_URI = 'sqlite:///money_tracker.db'
    SQLALCHEMY_TRACK_MODIFICATIONS = False
    
    # サーバー設定
    HOST_IP = os.getenv('HOST_IP', '127.0.0.1')  # デフォルトはlocalhostのみ
    
    # ログ設定
    LOG_LEVEL = os.getenv('LOG_LEVEL', 'INFO').upper()
    ENVIRONMENT = os.getenv('ENVIRONMENT', 'development').lower()
    
    @staticmethod
    def get_log_level():
        """ログレベルを取得
        
        Returns:
            int: ログレベルの定数
        """
        log_level_mapping = {
            'DEBUG': logging.DEBUG,
            'INFO': logging.INFO,
            'WARNING': logging.WARNING,
            'ERROR': logging.ERROR,
            'CRITICAL': logging.CRITICAL
        }
        return log_level_mapping.get(Config.LOG_LEVEL, logging.INFO)
    
    @staticmethod
    def is_production():
        """本番環境かどうか判定
        
        Returns:
            bool: 本番環境の場合True
        """
        return Config.ENVIRONMENT == 'production'

def setup_logging(app):
    """ログシステムを設定
    
    Args:
        app: Flaskアプリケーションインスタンス
    """
    # ログディレクトリを作成
    if not os.path.exists('logs'):
        os.makedirs('logs')
    
    log_level = Config.get_log_level()
    is_production = Config.is_production()
    
    # ログフォーマット
    formatter = logging.Formatter(
        '%(asctime)s %(levelname)s [%(filename)s:%(lineno)d] %(message)s'
    )
    
    # ファイルハンドラー(ローテーション付き)(常に有効)
    file_handler = RotatingFileHandler(
        'logs/money_tracker.log',
        maxBytes=10*1024*1024,  # 10MB
        backupCount=5
    )
    file_handler.setFormatter(formatter)
    file_handler.setLevel(log_level)
    app.logger.addHandler(file_handler)
    
    # コンソールハンドラー(開発環境のみ)
    if not is_production:
        console_handler = logging.StreamHandler()
        console_handler.setFormatter(formatter)
        console_handler.setLevel(log_level)
        app.logger.addHandler(console_handler)
    
    # アプリケーションロガー設定
    app.logger.setLevel(log_level)
    
    # Werkzeugのログレベルを調整（本番環境では不要なログを抑制）
    werkzeug_level = logging.WARNING if log_level >= logging.INFO else logging.INFO
    logging.getLogger('werkzeug').setLevel(werkzeug_level)
    
    environment_type = "本番環境" if is_production else "開発環境"
    app.logger.info(f"ログシステムを初期化しました ({environment_type}, レベル: {logging.getLevelName(log_level)})")

def init_config(app):
    """アプリケーション設定を初期化
    
    Args:
        app: Flaskアプリケーションインスタンス
    """
    app.config.from_object(Config)
    
    # デバッグ情報をログ出力
    app.logger.info(f"現在の作業ディレクトリ: {os.getcwd()}")
    app.logger.info(f"データベースURI: {app.config['SQLALCHEMY_DATABASE_URI']}")
    app.logger.info(f"環境設定: {Config.ENVIRONMENT}")
    app.logger.info(f"ログレベル: {Config.LOG_LEVEL}")